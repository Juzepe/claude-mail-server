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
// credentials and lists messages in the given folder. page is 1-indexed;
// returns the emails for that page, all folder names, and the total page count.
func fetchEmailsForUser(email, password, folder string, page int) ([]EmailMessage, []string, int, error) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return nil, nil, 0, fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return nil, nil, 0, fmt.Errorf("IMAP login failed: %w", err)
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
		return nil, folders, 0, fmt.Errorf("folder %q not found: %w", folder, err)
	}

	if mbox.Messages == 0 {
		return []EmailMessage{}, folders, 1, nil
	}

	// Pagination: newest messages first, 50 per page.
	const pageSize = 50
	n := int(mbox.Messages)
	totalPages := (n + pageSize - 1) / pageSize
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	toSeq := n - (page-1)*pageSize
	fromSeq := toSeq - pageSize + 1
	if fromSeq < 1 {
		fromSeq = 1
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddRange(uint32(fromSeq), uint32(toSeq))

	items := []goimap.FetchItem{
		goimap.FetchUid,
		goimap.FetchEnvelope,
		goimap.FetchFlags,
	}

	messages := make(chan *goimap.Message, pageSize)
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
			e.To = imapAddrsToString(msg.Envelope.To)
			e.CC = imapAddrsToString(msg.Envelope.Cc)
		}

		for _, flag := range msg.Flags {
			if flag == goimap.SeenFlag {
				e.Seen = true
			}
			if flag == goimap.FlaggedFlag {
				e.Flagged = true
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

	return emails, folders, totalPages, nil
}

// fetchEmailHeaderByUID fetches envelope + flags for a single message by UID.
func fetchEmailHeaderByUID(email, password, folder string, uid uint32) (*EmailMessage, error) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return nil, fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return nil, fmt.Errorf("IMAP login failed: %w", err)
	}

	if _, err := c.Select(folder, true); err != nil {
		return nil, fmt.Errorf("folder %q not found: %w", folder, err)
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddNum(uid)

	items := []goimap.FetchItem{goimap.FetchUid, goimap.FetchEnvelope, goimap.FetchFlags}
	messages := make(chan *goimap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqSet, items, messages)
	}()

	var em EmailMessage
	for msg := range messages {
		em.UID = msg.Uid
		em.Date = time.Now()
		if msg.Envelope != nil {
			em.Subject = msg.Envelope.Subject
			if msg.Envelope.Date != (time.Time{}) {
				em.Date = msg.Envelope.Date
			}
			if len(msg.Envelope.From) > 0 && msg.Envelope.From[0] != nil {
				addr := msg.Envelope.From[0]
				if addr.PersonalName != "" {
					em.From = fmt.Sprintf("%s <%s@%s>", addr.PersonalName, addr.MailboxName, addr.HostName)
				} else {
					em.From = fmt.Sprintf("%s@%s", addr.MailboxName, addr.HostName)
				}
			}
			em.To = imapAddrsToString(msg.Envelope.To)
			em.CC = imapAddrsToString(msg.Envelope.Cc)
		}
		for _, flag := range msg.Flags {
			if flag == goimap.SeenFlag {
				em.Seen = true
			}
			if flag == goimap.FlaggedFlag {
				em.Flagged = true
			}
		}
	}
	if err := <-done; err != nil {
		log.Printf("Portal: header fetch warning: %v", err)
	}

	if em.UID == 0 {
		return nil, fmt.Errorf("message UID %d not found", uid)
	}
	return &em, nil
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
// authenticates with PlainAuth, and sends the message. to/cc/bcc may each be
// comma-separated. Returns the raw RFC 2822 message bytes for Sent-folder saving.
func sendEmailViaLocalSMTP(from, password, to, cc, bcc, subject, body string) ([]byte, error) {
	c, err := smtp.Dial("localhost:587")
	if err != nil {
		return nil, fmt.Errorf("SMTP dial failed: %w", err)
	}
	defer c.Close()

	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // intentional: connecting to localhost
		ServerName:         "localhost",
	}
	if err := c.StartTLS(tlsCfg); err != nil {
		return nil, fmt.Errorf("STARTTLS failed: %w", err)
	}

	auth := smtp.PlainAuth("", from, password, "localhost")
	if err := c.Auth(auth); err != nil {
		return nil, fmt.Errorf("SMTP auth failed: %w", err)
	}

	if err := c.Mail(from); err != nil {
		return nil, fmt.Errorf("SMTP MAIL FROM failed: %w", err)
	}

	// RCPT TO for every address (To + CC + BCC all get the message)
	allRcpts := append(append(splitAddresses(to), splitAddresses(cc)...), splitAddresses(bcc)...)
	if len(allRcpts) == 0 {
		return nil, fmt.Errorf("no recipients specified")
	}
	for _, rcpt := range allRcpts {
		if err := c.Rcpt(rcpt); err != nil {
			return nil, fmt.Errorf("SMTP RCPT TO %s failed: %w", rcpt, err)
		}
	}

	wc, err := c.Data()
	if err != nil {
		return nil, fmt.Errorf("SMTP DATA failed: %w", err)
	}

	// Build headers (BCC intentionally omitted from headers)
	var hdr strings.Builder
	hdr.WriteString(fmt.Sprintf("From: %s\r\n", from))
	hdr.WriteString(fmt.Sprintf("To: %s\r\n", to))
	if cc != "" {
		hdr.WriteString(fmt.Sprintf("Cc: %s\r\n", cc))
	}
	hdr.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	hdr.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format("Mon, 02 Jan 2006 15:04:05 -0700")))
	hdr.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n")
	rawMsg := []byte(hdr.String() + body)

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

// deleteEmail moves a message to Trash. If the message is already in Trash it
// is permanently deleted.
func deleteEmail(email, password, folder string, uid uint32) error {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return fmt.Errorf("IMAP login failed: %w", err)
	}

	if _, err := c.Select(folder, false); err != nil {
		return fmt.Errorf("select folder failed: %w", err)
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddNum(uid)

	if !strings.EqualFold(folder, "Trash") {
		// Ensure Trash exists
		if err := c.Create("Trash"); err != nil && !strings.Contains(err.Error(), "exist") {
			log.Printf("Portal: create Trash failed: %v", err)
		}
		if err := c.UidCopy(seqSet, "Trash"); err != nil {
			return fmt.Errorf("copy to Trash failed: %w", err)
		}
	}

	item := goimap.FormatFlagsOp(goimap.AddFlags, true)
	flags := []interface{}{goimap.DeletedFlag}
	if err := c.UidStore(seqSet, item, flags, nil); err != nil {
		return fmt.Errorf("mark deleted failed: %w", err)
	}
	return c.Expunge(nil)
}

// markEmailSeen sets or clears the \Seen flag on a message.
func markEmailSeen(email, password, folder string, uid uint32, seen bool) error {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return fmt.Errorf("IMAP login failed: %w", err)
	}

	if _, err := c.Select(folder, false); err != nil {
		return fmt.Errorf("select folder failed: %w", err)
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddNum(uid)

	var op goimap.FlagsOp = goimap.AddFlags
	if !seen {
		op = goimap.RemoveFlags
	}
	item := goimap.FormatFlagsOp(op, true)
	flags := []interface{}{goimap.SeenFlag}
	return c.UidStore(seqSet, item, flags, nil)
}

// toggleEmailFlagged sets or clears the \Flagged flag on a message.
func toggleEmailFlagged(email, password, folder string, uid uint32, flagged bool) error {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return fmt.Errorf("IMAP login failed: %w", err)
	}

	if _, err := c.Select(folder, false); err != nil {
		return fmt.Errorf("select folder failed: %w", err)
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddNum(uid)

	var op goimap.FlagsOp = goimap.AddFlags
	if !flagged {
		op = goimap.RemoveFlags
	}
	item := goimap.FormatFlagsOp(op, true)
	flags := []interface{}{goimap.FlaggedFlag}
	return c.UidStore(seqSet, item, flags, nil)
}

// fetchStarredEmails searches all folders for messages with the \Flagged flag.
func fetchStarredEmails(email, password string) ([]EmailMessage, error) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return nil, fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return nil, fmt.Errorf("IMAP login failed: %w", err)
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
		log.Printf("Portal: starred list warning: %v", err)
	}

	var allEmails []EmailMessage
	for _, folder := range folders {
		mbox, err := c.Select(folder, true)
		if err != nil || mbox.Messages == 0 {
			continue
		}

		criteria := goimap.NewSearchCriteria()
		criteria.WithFlags = []string{goimap.FlaggedFlag}
		uids, err := c.UidSearch(criteria)
		if err != nil || len(uids) == 0 {
			continue
		}

		seqSet := new(goimap.SeqSet)
		for _, uid := range uids {
			seqSet.AddNum(uid)
		}

		items := []goimap.FetchItem{goimap.FetchUid, goimap.FetchEnvelope, goimap.FetchFlags}
		messages := make(chan *goimap.Message, len(uids))
		fetchDone := make(chan error, 1)
		go func() {
			fetchDone <- c.UidFetch(seqSet, items, messages)
		}()

		for msg := range messages {
			e := EmailMessage{
				UID:     msg.Uid,
				Folder:  folder,
				Flagged: true,
				Date:    time.Now(),
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
				e.To = imapAddrsToString(msg.Envelope.To)
				e.CC = imapAddrsToString(msg.Envelope.Cc)
			}
			for _, flag := range msg.Flags {
				if flag == goimap.SeenFlag {
					e.Seen = true
				}
			}
			allEmails = append(allEmails, e)
		}
		if err := <-fetchDone; err != nil {
			log.Printf("Portal: starred fetch warning (%s): %v", folder, err)
		}
	}

	// Sort newest first
	for i := 0; i < len(allEmails)-1; i++ {
		for j := i + 1; j < len(allEmails); j++ {
			if allEmails[j].Date.After(allEmails[i].Date) {
				allEmails[i], allEmails[j] = allEmails[j], allEmails[i]
			}
		}
	}
	return allEmails, nil
}

// moveEmail copies a message to toFolder then removes it from fromFolder.
func moveEmail(email, password, fromFolder, toFolder string, uid uint32) error {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return fmt.Errorf("IMAP login failed: %w", err)
	}

	if _, err := c.Select(fromFolder, false); err != nil {
		return fmt.Errorf("select folder failed: %w", err)
	}

	// Ensure destination exists
	if err := c.Create(toFolder); err != nil && !strings.Contains(err.Error(), "exist") {
		log.Printf("Portal: create folder %q failed: %v", toFolder, err)
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddNum(uid)

	if err := c.UidCopy(seqSet, toFolder); err != nil {
		return fmt.Errorf("copy to %q failed: %w", toFolder, err)
	}

	item := goimap.FormatFlagsOp(goimap.AddFlags, true)
	flags := []interface{}{goimap.DeletedFlag}
	if err := c.UidStore(seqSet, item, flags, nil); err != nil {
		return fmt.Errorf("mark deleted failed: %w", err)
	}
	return c.Expunge(nil)
}

// emptyFolder permanently deletes all messages in the given folder.
func emptyFolder(email, password, folder string) error {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		return fmt.Errorf("IMAP login failed: %w", err)
	}

	mbox, err := c.Select(folder, false)
	if err != nil {
		return fmt.Errorf("select folder failed: %w", err)
	}
	if mbox.Messages == 0 {
		return nil
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddRange(1, mbox.Messages)

	item := goimap.FormatFlagsOp(goimap.AddFlags, true)
	flags := []interface{}{goimap.DeletedFlag}
	if err := c.Store(seqSet, item, flags, nil); err != nil {
		return fmt.Errorf("mark deleted failed: %w", err)
	}
	return c.Expunge(nil)
}

// imapAddrsToString formats a slice of IMAP addresses as a comma-separated string.
func imapAddrsToString(addrs []*goimap.Address) string {
	var parts []string
	for _, addr := range addrs {
		if addr == nil {
			continue
		}
		if addr.PersonalName != "" {
			parts = append(parts, fmt.Sprintf("%s <%s@%s>", addr.PersonalName, addr.MailboxName, addr.HostName))
		} else {
			parts = append(parts, fmt.Sprintf("%s@%s", addr.MailboxName, addr.HostName))
		}
	}
	return strings.Join(parts, ", ")
}

// splitAddresses splits a comma-separated address string into trimmed, non-empty parts.
func splitAddresses(s string) []string {
	var result []string
	for _, addr := range strings.Split(s, ",") {
		addr = strings.TrimSpace(addr)
		if addr != "" {
			result = append(result, addr)
		}
	}
	return result
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
		"inc": func(i int) int { return i + 1 },
		"dec": func(i int) int { return i - 1 },
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
		case path == "/forward":
			portalForward(w, r, cfg, sess)
		case path == "/delete":
			portalDelete(w, r, cfg, sess)
		case path == "/mark":
			portalMarkSeen(w, r, cfg, sess)
		case path == "/move":
			portalMove(w, r, cfg, sess)
		case path == "/empty-trash":
			portalEmptyTrash(w, r, cfg, sess)
		case path == "/star":
			portalStar(w, r, cfg, sess)
		case path == "/starred":
			portalStarred(w, r, cfg, sess)
		case path == "/credentials":
			portalCredentials(w, r, cfg, sess)
		case path == "/clients":
			portalClients(w, r, cfg, sess)
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
	Page          int
	TotalPages    int
}

func portalInbox(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	q := r.URL.Query()
	folder := q.Get("folder")
	if folder == "" {
		folder = "INBOX"
	}
	if folder == "Starred" {
		http.Redirect(w, r, "/starred", http.StatusSeeOther)
		return
	}
	uidStr := q.Get("uid")
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}

	data := portalInboxData{
		Domain:  cfg.Domain,
		Email:   sess.Email,
		Folder:  folder,
		Folders: []string{"INBOX", "Sent", "Drafts", "Trash", "Junk"},
		Page:    page,
	}

	emails, folders, totalPages, err := fetchEmailsForUser(sess.Email, sess.Password, folder, page)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load mailbox: %v", err)
		renderPortalTemplate(w, "portal_inbox.html", data)
		return
	}

	data.Emails = emails
	data.TotalPages = totalPages
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
						emails[i].Seen = true // optimistic update for template
						data.SelectedEmail = &emails[i]
						// Auto-mark as read when opening
						if !e.Seen {
							go markEmailSeen(sess.Email, sess.Password, folder, uid, true)
						}
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
	CC         string
	BCC        string
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
		cc := strings.TrimSpace(r.FormValue("cc"))
		bcc := strings.TrimSpace(r.FormValue("bcc"))
		subject := strings.TrimSpace(r.FormValue("subject"))
		body := r.FormValue("body")
		quotedBody := r.FormValue("quoted_body")

		log.Printf("Portal: compose sending from=%s to=%s subject=%q", sess.Email, to, subject)

		data.To = to
		data.CC = cc
		data.BCC = bcc
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

		rawMsg, err := sendEmailViaLocalSMTP(sess.Email, sess.Password, to, cc, bcc, subject, body)
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
		data.CC = ""
		data.BCC = ""
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

	original, err := fetchEmailHeaderByUID(sess.Email, sess.Password, folder, uid)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	body, _ := fetchBodyForUser(sess.Email, sess.Password, folder, uid)

	replyTo := extractEmailAddress(original.From)
	replySubject := original.Subject
	if !strings.HasPrefix(strings.ToLower(replySubject), "re:") {
		replySubject = "Re: " + replySubject
	}

	// Reply-all: include all original recipients (To + CC) except self
	var replyCC string
	if q.Get("all") == "1" {
		var ccAddrs []string
		for _, addr := range splitAddresses(original.To + "," + original.CC) {
			if addr != "" && addr != sess.Email {
				ccAddrs = append(ccAddrs, addr)
			}
		}
		replyCC = strings.Join(ccAddrs, ", ")
	}

	data := portalComposeData{
		Domain:     cfg.Domain,
		Email:      sess.Email,
		To:         replyTo,
		CC:         replyCC,
		Subject:    replySubject,
		QuotedBody: body,
	}
	renderPortalTemplate(w, "portal_compose.html", data)
}

func portalDelete(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	folder := r.FormValue("folder")
	if folder == "" {
		folder = "INBOX"
	}
	uidStr := r.FormValue("uid")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		http.Redirect(w, r, "/inbox?folder="+folder, http.StatusSeeOther)
		return
	}

	if err := deleteEmail(sess.Email, sess.Password, folder, uint32(uid64)); err != nil {
		log.Printf("Portal: delete error for %s: %v", sess.Email, err)
	}
	http.Redirect(w, r, "/inbox?folder="+folder, http.StatusSeeOther)
}

func portalMarkSeen(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	folder := r.FormValue("folder")
	if folder == "" {
		folder = "INBOX"
	}
	uidStr := r.FormValue("uid")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		http.Redirect(w, r, "/inbox?folder="+folder, http.StatusSeeOther)
		return
	}
	seen := r.FormValue("seen") == "true"

	if err := markEmailSeen(sess.Email, sess.Password, folder, uint32(uid64), seen); err != nil {
		log.Printf("Portal: mark seen error for %s: %v", sess.Email, err)
	}
	http.Redirect(w, r, "/inbox?folder="+folder+"&uid="+uidStr, http.StatusSeeOther)
}

func portalForward(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
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

	original, err := fetchEmailHeaderByUID(sess.Email, sess.Password, folder, uid)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	body, _ := fetchBodyForUser(sess.Email, sess.Password, folder, uid)

	fwdSubject := original.Subject
	lower := strings.ToLower(fwdSubject)
	if !strings.HasPrefix(lower, "fwd:") && !strings.HasPrefix(lower, "fw:") {
		fwdSubject = "Fwd: " + fwdSubject
	}

	data := portalComposeData{
		Domain:     cfg.Domain,
		Email:      sess.Email,
		Subject:    fwdSubject,
		QuotedBody: body,
	}
	renderPortalTemplate(w, "portal_compose.html", data)
}

func portalMove(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	fromFolder := r.FormValue("folder")
	if fromFolder == "" {
		fromFolder = "INBOX"
	}
	toFolder := r.FormValue("to_folder")
	if toFolder == "" || toFolder == fromFolder {
		http.Redirect(w, r, "/inbox?folder="+fromFolder, http.StatusSeeOther)
		return
	}
	uidStr := r.FormValue("uid")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		http.Redirect(w, r, "/inbox?folder="+fromFolder, http.StatusSeeOther)
		return
	}

	if err := moveEmail(sess.Email, sess.Password, fromFolder, toFolder, uint32(uid64)); err != nil {
		log.Printf("Portal: move error for %s: %v", sess.Email, err)
	}
	http.Redirect(w, r, "/inbox?folder="+fromFolder, http.StatusSeeOther)
}

func portalEmptyTrash(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/inbox?folder=Trash", http.StatusSeeOther)
		return
	}
	if err := emptyFolder(sess.Email, sess.Password, "Trash"); err != nil {
		log.Printf("Portal: empty trash error for %s: %v", sess.Email, err)
	}
	http.Redirect(w, r, "/inbox?folder=Trash", http.StatusSeeOther)
}

func portalStar(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	folder := r.FormValue("folder")
	if folder == "" {
		folder = "INBOX"
	}
	uidStr := r.FormValue("uid")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		http.Redirect(w, r, "/inbox?folder="+folder, http.StatusSeeOther)
		return
	}
	flagged := r.FormValue("flagged") == "true"
	if err := toggleEmailFlagged(sess.Email, sess.Password, folder, uint32(uid64), flagged); err != nil {
		log.Printf("Portal: star error for %s: %v", sess.Email, err)
	}
	redirectTo := r.FormValue("redirect")
	if redirectTo == "" {
		redirectTo = "/inbox?folder=" + folder
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

func portalStarred(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	data := portalInboxData{
		Domain:     cfg.Domain,
		Email:      sess.Email,
		Folder:     "Starred",
		Folders:    []string{"INBOX", "Sent", "Drafts", "Trash", "Junk"},
		Page:       1,
		TotalPages: 1,
	}

	emails, err := fetchStarredEmails(sess.Email, sess.Password)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load starred emails: %v", err)
		renderPortalTemplate(w, "portal_inbox.html", data)
		return
	}
	data.Emails = emails

	uidStr := r.URL.Query().Get("uid")
	if uidStr != "" {
		uid64, err := strconv.ParseUint(uidStr, 10, 32)
		if err == nil {
			uid := uint32(uid64)
			for i, e := range emails {
				if e.UID == uid {
					srcFolder := e.Folder
					if srcFolder == "" {
						srcFolder = "INBOX"
					}
					body, err := fetchBodyForUser(sess.Email, sess.Password, srcFolder, uid)
					if err == nil {
						emails[i].Body = body
						emails[i].Seen = true
						data.SelectedEmail = &emails[i]
						if !e.Seen {
							go markEmailSeen(sess.Email, sess.Password, srcFolder, uid, true)
						}
					}
					break
				}
			}
		}
	}

	renderPortalTemplate(w, "portal_inbox.html", data)
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
	WebmailURL  string
	LaravelEnv  string
	PHPExample  string
	NodeExample string
}

func portalClients(w http.ResponseWriter, r *http.Request, cfg *config.Config, sess *db.UserSession) {
	data := portalCredentialsData{
		Domain:     cfg.Domain,
		Email:      sess.Email,
		SMTPHost:   cfg.Hostname,
		IMAPHost:   cfg.Hostname,
		WebmailURL: "https://webmail." + cfg.Domain,
	}
	renderPortalTemplate(w, "portal_clients.html", data)
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
