package handlers

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"mailserver/config"
	"mailserver/db"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "mailserver_session"

// templateDir returns the path to the templates directory.
func templateDir() string {
	return "/opt/mailserver/web/templates"
}

// Login handles GET /login (show form) and POST /login (authenticate).
func Login(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If already logged in, redirect to dashboard
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			if db.ValidateSession(cookie.Value) {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}

		switch r.Method {
		case http.MethodGet:
			renderLogin(w, cfg, "", "")

		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}

			email := strings.TrimSpace(r.FormValue("email"))
			password := r.FormValue("password")

			// Validate credentials
			if !authenticate(email, password, cfg) {
				log.Printf("Failed login attempt for %s from %s", email, getIP(r))
				db.LogAction("login_failed", email, "", getIP(r))
				renderLogin(w, cfg, email, "Invalid email or password.")
				return
			}

			// Create session
			token, err := db.CreateSession()
			if err != nil {
				log.Printf("Failed to create session: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    token,
				Path:     "/",
				Expires:  time.Now().Add(24 * time.Hour),
				HttpOnly: true,
				Secure:   r.TLS != nil,
				SameSite: http.SameSiteLaxMode,
			})

			db.LogAction("login", email, "", getIP(r))
			log.Printf("Successful login for %s from %s", email, getIP(r))
			http.Redirect(w, r, "/", http.StatusSeeOther)

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// Logout handles GET /logout - clears the session cookie.
func Logout(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			db.DeleteSession(cookie.Value)
			db.LogAction("logout", cfg.AdminEmail, "", getIP(r))
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
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

// authenticate checks if the provided credentials match the configured admin.
func authenticate(email, password string, cfg *config.Config) bool {
	if email != cfg.AdminEmail {
		return false
	}
	if password == "" {
		return false
	}
	// Compare against bcrypt hash
	err := bcrypt.CompareHashAndPassword([]byte(cfg.AdminPasswordHash), []byte(password))
	return err == nil
}

type loginData struct {
	Domain   string
	Email    string
	Error    string
}

func renderLogin(w http.ResponseWriter, cfg *config.Config, email, errMsg string) {
	tmplPath := templateDir() + "/login.html"
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := loginData{
		Domain: cfg.Domain,
		Email:  email,
		Error:  errMsg,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Template execution error: %v", err)
	}
}

// getIP extracts the real client IP from a request.
func getIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	// Strip port
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		addr = addr[:idx]
	}
	return addr
}

// GetSessionToken extracts the session token from the request cookie.
func GetSessionToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}
