package handlers

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/smtp"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mailserver/config"
	"mailserver/db"

	goimap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const portalSessionCookieName = "portal_session"

// ---- Session helpers -------------------------------------------------------

func getPortalSession(r *http.Request) *db.UserSession {
	cookie, err := r.Cookie(portalSessionCookieName)
	if err != nil {
		return nil
	}
	sess, ok := db.GetUserSession(cookie.Value)
	if !ok {
		return nil
	}
	return sess
}

// ---- IMAP auth + fetch helpers ---------------------------------------------

// authenticateIMAPUser tries to log in to the local IMAP server with the
// provided credentials. Returns true on success.
func authenticateIMAPUser(email, password string) bool {
	c, err := client.Dial("localhost:143")
	if err != nil {
		log.Printf("Portal IMAP dial error: %v", err)
		return false
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return false
	}
	return true
}

// fetchEmailsForUser connects to the local IMAP server with the user's own
// credentials and lists messages in the given folder.
func fetchEmailsForUser(email, password, folder string) ([]EmailMessage, []string, error) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return nil, nil, fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return nil, nil, fmt.Errorf("IMAP login failed: %w", err)
	}

	// List mailboxes
	mailboxes := make(chan *goimap.MailboxInfo, 20)
	listDone := make(chan error, 1)
	go func() {
		listDone <- c.List("", "*", mailboxes)
	}()

	var folders []string
	for m := range mailboxes {
		folders = append(folders, m.Name)
	}
	if err := <-listDone; err != nil {
		log.Printf("Portal: mailbox list warning: %v", err)
	}

	// Select the folder
	mbox, err := c.Select(folder, true)
	if err != nil {
		return nil, folders, fmt.Errorf("folder %q not found: %w", folder, err)
	}

	if mbox.Messages == 0 {
		return []EmailMessage{}, folders, nil
	}

	// Fetch last 50 messages
	from := uint32(1)
	if mbox.Messages > 50 {
		from = mbox.Messages - 49
	}
	seqSet := new(goimap.SeqSet)
	seqSet.AddRange(from, mbox.Messages)

	items := []goimap.FetchItem{
		goimap.FetchUid,
		goimap.FetchEnvelope,
		goimap.FetchFlags,
	}

	messages := make(chan *goimap.Message, 50)
	fetchDone := make(chan error, 1)
	go func() {
		fetchDone <- c.Fetch(seqSet, items, messages)
	}()

	var emails []EmailMessage
	for msg := range messages {
		e := EmailMessage{
			UID:  msg.Uid,
			Date: time.Now(),
		}

		if msg.Envelope != nil {
			e.Subject = msg.Envelope.Subject
			if msg.Envelope.Date != (time.Time{}) {
				e.Date = msg.Envelope.Date
			}
			if len(msg.Envelope.From) > 0 && msg.Envelope.From[0] != nil {
				addr := msg.Envelope.From[0]
				if addr.PersonalName != "" {
					e.From = fmt.Sprintf("%s <%s@%s>", addr.PersonalName, addr.MailboxName, addr.HostName)
				} else {
					e.From = fmt.Sprintf("%s@%s", addr.MailboxName, addr.HostName)
				}
			}
		}

		for _, flag := range msg.Flags {
			if flag == goimap.SeenFlag {
				e.Seen = true
				break
			}
		}

		emails = append(emails, e)
	}

	if err := <-fetchDone; err != nil {
		log.Printf("Portal: fetch warning: %v", err)
	}

	// Reverse so newest first
	for i, j := 0, len(emails)-1; i < j; i, j = i+1, j-1 {
		emails[i], emails[j] = emails[j], emails[i]
	}

	return emails, folders, nil
}

// fetchBodyForUser fetches the full body of a single message using the user's
// own IMAP credentials.
func fetchBodyForUser(email, password, folder string, uid uint32) (string, error) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return "", fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return "", fmt.Errorf("IMAP login failed: %w", err)
	}

	if _, err := c.Select(folder, true); err != nil {
		return "", fmt.Errorf("folder %q not found: %w", folder, err)
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddNum(uid)

	section := &goimap.BodySectionName{}
	items := []goimap.FetchItem{section.FetchItem()}

	messages := make(chan *goimap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqSet, items, messages)
	}()

	var bodyStr string
	for msg := range messages {
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		bodyBytes, err := io.ReadAll(r)
		if err == nil {
			bodyStr = string(bodyBytes)
		}
	}

	if err := <-done; err != nil {
		log.Printf("Portal: uid fetch warning: %v", err)
	}

	bodyStr = extractPlainText(bodyStr)

	const maxBodyLen = 50000
	if len(bodyStr) > maxBodyLen {
		bodyStr = bodyStr[:maxBodyLen] + "\n\n[Message truncated...]"
	}

	return bodyStr, nil
}

// extractPlainText parses a raw RFC 2822 message and returns the text/plain
// body, decoding quoted-printable and handling multipart messages.
func extractPlainText(raw string) string {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return raw
	}

	contentType := msg.Header.Get("Content-Type")
	if contentType == "" {
		body, _ := io.ReadAll(msg.Body)
		return string(body)
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		body, _ := io.ReadAll(msg.Body)
		return string(body)
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			partMediaType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if partMediaType == "text/plain" {
				return decodePart(part, part.Header.Get("Content-Transfer-Encoding"))
			}
		}
		return ""
	}

	return decodePart(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
}

func decodePart(r io.Reader, encoding string) string {
	if strings.EqualFold(encoding, "quoted-printable") {
		r = quotedprintable.NewReader(r)
	}
	body, _ := io.ReadAll(r)
	return string(body)
}

// ---- SMTP send helper ------------------------------------------------------

// sendEmailViaLocalSMTP dials localhost:587, does STARTTLS (InsecureSkipVerify),
// authenticates with PlainAuth, and sends the message. Returns the raw RFC 2822
// message bytes so the caller can save a copy to the Sent folder.
func sendEmailViaLocalSMTP(from, password, to, subject, body string) ([]byte, error) {
	c, err := smtp.Dial("localhost:587")
	if err != nil {
		return nil, fmt.Errorf("SMTP dial failed: %w", err)
	}
	defer c.Close()

	// Issue STARTTLS
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // intentional: connecting to localhost
		ServerName:         "localhost",
	}
	if err := c.StartTLS(tlsCfg); err != nil {
		return nil, fmt.Errorf("STARTTLS failed: %w", err)
	}

	// Authenticate
	auth := smtp.PlainAuth("", from, password, "localhost")
	if err := c.Auth(auth); err != nil {
		return nil, fmt.Errorf("SMTP auth failed: %w", err)
	}

	// Envelope
	if err := c.Mail(from); err != nil {
		return nil, fmt.Errorf("SMTP MAIL FROM failed: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return nil, fmt.Errorf("SMTP RCPT TO failed: %w", err)
	}

	// Message data
	wc, err := c.Data()
	if err != nil {
		return nil, fmt.Errorf("SMTP DATA failed: %w", err)
	}

	rawMsg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		from, to, subject, time.Now().Format("Mon, 02 Jan 2006 15:04:05 -0700"), body))
	if _, err := wc.Write(rawMsg); err != nil {
		wc.Close()
		return nil, fmt.Errorf("SMTP write failed: %w", err)
	}
	if err := wc.Close(); err != nil {
		return nil, fmt.Errorf("SMTP data close failed: %w", err)
	}

	return rawMsg, c.Quit()
}

// appendToSent saves a copy of the message to the user's Sent folder via IMAP APPEND.
// It creates the Sent mailbox first if it does not exist.
func appendToSent(email, password string, rawMsg []byte) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		log.Printf("Portal: IMAP dial for Sent append failed: %v", err)
		return
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		log.Printf("Portal: IMAP login for Sent append failed: %v", err)
		return
	}

	// Ensure Sent mailbox exists
	if err := c.Create("Sent"); err != nil {
		// Ignore "already exists" errors
		if !strings.Contains(err.Error(), "exist") {
			log.Printf("Portal: IMAP Create Sent failed: %v", err)
		}
	}

	flags := []string{goimap.SeenFlag}
	if err := c.Append("Sent", flags, time.Now(), bytes.NewReader(rawMsg)); err != nil {
		log.Printf("Portal: IMAP Append to Sent failed: %v", err)
	} else {
		log.Printf("Portal: saved sent email to Sent folder for %s", email)
	}
}

// ---- Template rendering helpers --------------------------------------------

func portalFuncMap() template.FuncMap {
	return template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.Format("Jan 2, 15:04")
		},
		"statusClass": func(active bool) string {
			if active {
				return "status-up"
			}
			return "status-down"
		},
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
	}
}

// renderPortalLogin renders the standalone portal_login.html (no layout).
func renderPortalLogin(w http.ResponseWriter, cfg *config.Config, email, errMsg string) {
	tmplPath := filepath.Join(templateDir(), "portal_login.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		log.Printf("Portal login template error: %v", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		Domain string
		Email  string
		Error  string
	}{
		Domain: cfg.Domain,
		Email:  email,
		Error:  errMsg,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Portal login template execute error: %v", err)
	}
}

// renderPortalTemplate renders a portal page template inside portal_layout.html.
func renderPortalTemplate(w http.ResponseWriter, name string, data interface{}) {
	dir := templateDir()
	layoutPath := filepath.Join(dir, "portal_layout.html")
	pagePath := filepath.Join(dir, name)

	tmpl, err := template.New("portal_layout.html").Funcs(portalFuncMap()).ParseFiles(layoutPath, pagePath)
	if err != nil {
		log.Printf("Portal template parse error (%s): %v", name, err)
		http.Error(w, fmt.Sprintf("Template error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "portal_layout.html", data); err != nil {
		log.Printf("Portal template execute error (%s): %v", name, err)
	}
}

// ---- Route handlers --------------------------------------------------------

// PortalLogin handles GET /login (show form) and POST /login (authenticate).
func PortalLogin(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If already logged in, redirect to inbox
		if sess := getPortalSession(r); sess != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		switch r.Method {
		case http.MethodGet:
			renderPortalLogin(w, cfg, "", "")

		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}

			email := strings.TrimSpace(r.FormValue("email"))
			password := r.FormValue("password")

			if !authenticateIMAPUser(email, password) {
				log.Printf("Portal: failed login for %s from %s", email, getIP(r))
				renderPortalLogin(w, cfg, email, "Invalid email or password.")
				return
			}

			token, err := db.CreateUserSession(email, password)
			if err != nil {
				log.Printf("Portal: failed to create session: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			http.SetCookie(w, &http.Cookie{
				Name:     portalSessionCookieName,
				Value:    token,
				Path:     "/",
				Expires:  time.Now().Add(24 * time.Hour),
				HttpOnly: true,
				Secure:   r.TLS != nil,
				SameSite: http.SameSiteLaxMode,
			})

			log.Printf("Portal: successful login for %s from %s", email, getIP(r))
			http.Redirect(w, r, "/", http.StatusSeeOther)

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// PortalLogout handles GET /portal/logout — deletes the session and redirects to login.
func PortalLogout(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(portalSessionCookieName); err == nil {
			db.DeleteUserSession(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     portalSessionCookieName,
			Value:    "",
			Path:     "/",
			Expires:  time.Unix(0, 0),
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   r.TLS != nil,
		})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// PortalHandler handles all authenticated portal routes at the root of the portal subdomain.
func PortalHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := getPortalSession(r)
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		path := r.URL.Path
		switch {
		case path == "/" || path == "/inbox":
			portalInbox(w, r, cfg, sess)
		case path == "/compose":
			portalCompose(w, r, cfg, sess)
		case path == "/reply":
			portalReply(w, r, cfg, sess)
		case path == "/credentials":
			portalCredentials(w, r, cfg, sess)
		default:
			http.NotFound(w, r)
		}
	}
}

// ---- Sub-page handlers -----------------------------------------------------

type portalInboxData struct {
	Domain        string
	Email         string
	Folder        string
	Folders       []string
	Emails        []EmailMessage
	SelectedEmail *EmailMessage
	Error         string
}

func portalInbox(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	q := r.URL.Query()
	folder := q.Get("folder")
	if folder == "" {
		folder = "INBOX"
	}
	uidStr := q.Get("uid")

	data := portalInboxData{
		Domain:  cfg.Domain,
		Email:   sess.Email,
		Folder:  folder,
		Folders: []string{"INBOX", "Sent", "Drafts", "Trash", "Junk"},
	}

	emails, folders, err := fetchEmailsForUser(sess.Email, sess.Password, folder)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load mailbox: %v", err)
		renderPortalTemplate(w, "portal_inbox.html", data)
		return
	}

	data.Emails = emails
	if len(folders) > 0 {
		data.Folders = folders
	}

	// Fetch body if a UID was requested
	if uidStr != "" {
		uid64, err := strconv.ParseUint(uidStr, 10, 32)
		if err == nil {
			uid := uint32(uid64)
			for i, e := range emails {
				if e.UID == uid {
					body, err := fetchBodyForUser(sess.Email, sess.Password, folder, uid)
					if err == nil {
						emails[i].Body = body
						data.SelectedEmail = &emails[i]
					}
					break
				}
			}
		}
	}

	renderPortalTemplate(w, "portal_inbox.html", data)
}

type portalComposeData struct {
	Domain     string
	Email      string
	Error      string
	Flash      string
	To         string
	Subject    string
	Body       string
	QuotedBody string // read-only original message shown below textarea
}

func portalCompose(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	data := portalComposeData{
		Domain: cfg.Domain,
		Email:  sess.Email,
	}

	log.Printf("Portal: compose %s %s", r.Method, sess.Email)
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			data.Error = "Failed to parse form."
			renderPortalTemplate(w, "portal_compose.html", data)
			return
		}

		to := strings.TrimSpace(r.FormValue("to"))
		subject := strings.TrimSpace(r.FormValue("subject"))
		body := r.FormValue("body")
		quotedBody := r.FormValue("quoted_body")

		log.Printf("Portal: compose sending from=%s to=%s subject=%q", sess.Email, to, subject)

		data.To = to
		data.Subject = subject
		data.Body = body
		data.QuotedBody = quotedBody

		if quotedBody != "" {
			body = body + "\n\n" + quotedBody
		}

		if to == "" {
			data.Error = "Recipient (To) is required."
			renderPortalTemplate(w, "portal_compose.html", data)
			return
		}
		if subject == "" {
			data.Error = "Subject is required."
			renderPortalTemplate(w, "portal_compose.html", data)
			return
		}

		rawMsg, err := sendEmailViaLocalSMTP(sess.Email, sess.Password, to, subject, body)
		if err != nil {
			log.Printf("Portal: send email error for %s: %v", sess.Email, err)
			data.Error = fmt.Sprintf("Failed to send email: %v", err)
			renderPortalTemplate(w, "portal_compose.html", data)
			return
		}
		log.Printf("Portal: email sent successfully from %s to %s", sess.Email, to)
		go appendToSent(sess.Email, sess.Password, rawMsg)

		// Success — clear fields and show flash
		data.To = ""
		data.Subject = ""
		data.Body = ""
		data.Flash = "Email sent successfully."
	}

	renderPortalTemplate(w, "portal_compose.html", data)
}

func portalReply(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	q := r.URL.Query()
	folder := q.Get("folder")
	if folder == "" {
		folder = "INBOX"
	}
	uidStr := q.Get("uid")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	uid := uint32(uid64)

	// Fetch the original email headers + body
	emails, _, err := fetchEmailsForUser(sess.Email, sess.Password, folder)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	var original *EmailMessage
	for i := range emails {
		if emails[i].UID == uid {
			original = &emails[i]
			break
		}
	}
	if original == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	body, _ := fetchBodyForUser(sess.Email, sess.Password, folder, uid)

	// Build reply fields
	replyTo := extractEmailAddress(original.From)
	replySubject := original.Subject
	if !strings.HasPrefix(strings.ToLower(replySubject), "re:") {
		replySubject = "Re: " + replySubject
	}

	data := portalComposeData{
		Domain:     cfg.Domain,
		Email:      sess.Email,
		To:         replyTo,
		Subject:    replySubject,
		QuotedBody: body,
	}
	renderPortalTemplate(w, "portal_compose.html", data)
}

// extractEmailAddress pulls the email address out of "Name <email>" or returns the string as-is.
func extractEmailAddress(from string) string {
	if start := strings.Index(from, "<"); start != -1 {
		if end := strings.Index(from, ">"); end > start {
			return from[start+1 : end]
		}
	}
	return from
}


type portalCredentialsData struct {
	Domain      string
	Email       string
	SMTPHost    string
	IMAPHost    string
	LaravelEnv  string
	PHPExample  string
	NodeExample string
}

func portalCredentials(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	smtpHost := cfg.Hostname
	imapHost := cfg.Hostname
	userEmail := sess.Email

	laravelEnv := `MAIL_MAILER=smtp
MAIL_HOST=` + smtpHost + `
MAIL_PORT=587
MAIL_USERNAME=` + userEmail + `
MAIL_PASSWORD=your_password
MAIL_ENCRYPTION=tls
MAIL_FROM_ADDRESS=` + userEmail + `
MAIL_FROM_NAME="${APP_NAME}"`

	phpExample := `<?php
// Using PHPMailer
use PHPMailer\PHPMailer\PHPMailer;
use PHPMailer\PHPMailer\SMTP;

$mail = new PHPMailer(true);
$mail->isSMTP();
$mail->Host       = '` + smtpHost + `';
$mail->SMTPAuth   = true;
$mail->Username   = '` + userEmail + `';
$mail->Password   = 'your_password';
$mail->SMTPSecure = PHPMailer::ENCRYPTION_STARTTLS;
$mail->Port       = 587;

$mail->setFrom('` + userEmail + `', 'Your Name');
$mail->addAddress('recipient@example.com');
$mail->Subject = 'Test Email';
$mail->Body    = 'Hello World!';
$mail->send();
?>`

	nodeExample := `// Using Nodemailer
const nodemailer = require('nodemailer');

const transporter = nodemailer.createTransport({
  host: '` + smtpHost + `',
  port: 587,
  secure: false, // STARTTLS
  auth: {
    user: '` + userEmail + `',
    pass: 'your_password'
  }
});

await transporter.sendMail({
  from: '"Your Name" <` + userEmail + `>',
  to: 'recipient@example.com',
  subject: 'Test Email',
  text: 'Hello World!'
});`

	data := portalCredentialsData{
		Domain:      cfg.Domain,
		Email:       userEmail,
		SMTPHost:    smtpHost,
		IMAPHost:    imapHost,
		LaravelEnv:  laravelEnv,
		PHPExample:  phpExample,
		NodeExample: nodeExample,
	}

	renderPortalTemplate(w, "portal_credentials.html", data)
}
