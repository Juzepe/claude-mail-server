package handlers

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"mailserver/config"
	"mailserver/db"

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

// ---- IMAP auth -------------------------------------------------------------

// authenticateIMAPUser verifies credentials against the local Dovecot IMAP server.
func authenticateIMAPUser(email, password string) bool {
	c, err := client.Dial("localhost:143")
	if err != nil {
		log.Printf("Portal IMAP dial error: %v", err)
		return false
	}
	defer c.Logout()
	return c.Login(email, password) == nil
}

// ---- Template helpers ------------------------------------------------------

func portalFuncMap() template.FuncMap {
	return template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.Format("Jan 2, 15:04")
		},
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
	}
}

func renderPortalLogin(w http.ResponseWriter, cfg *config.Config, email, errMsg string) {
	tmplPath := filepath.Join(templateDir(), "portal_login.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		Domain string
		Email  string
		Error  string
	}{cfg.Domain, email, errMsg}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Portal login template execute error: %v", err)
	}
}

func renderPortalTemplate(w http.ResponseWriter, name string, data interface{}) {
	dir := templateDir()
	tmpl, err := template.New("portal_layout.html").Funcs(portalFuncMap()).ParseFiles(
		filepath.Join(dir, "portal_layout.html"),
		filepath.Join(dir, name),
	)
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

// ---- Auth handlers ---------------------------------------------------------

func PortalLogin(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

func PortalLogout(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(portalSessionCookieName); err == nil {
			db.DeleteUserSession(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:    portalSessionCookieName,
			Value:   "",
			Path:    "/",
			Expires: time.Unix(0, 0),
			MaxAge:  -1,
		})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// ---- Main portal router ----------------------------------------------------

func PortalHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := getPortalSession(r)
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		switch r.URL.Path {
		case "/", "/credentials":
			portalCredentials(w, r, cfg, sess)
		case "/clients":
			portalClients(w, r, cfg, sess)
		default:
			http.NotFound(w, r)
		}
	}
}

// ---- Page handlers ---------------------------------------------------------

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
		WebmailURL:  "https://webmail." + cfg.Domain,
		LaravelEnv:  laravelEnv,
		PHPExample:  phpExample,
		NodeExample: nodeExample,
	}
	renderPortalTemplate(w, "portal_credentials.html", data)
}
