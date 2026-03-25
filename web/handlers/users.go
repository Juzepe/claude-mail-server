package handlers

import (
	"net/http"
	"strings"

	"mailserver/config"
	"mailserver/db"
	"mailserver/mail"
)

type usersData struct {
	Domain  string
	Users   []mail.MailUser
	Flash   string
	Error   string
}

// Users handles GET /users - lists all email accounts.
func Users(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := mail.ListUsers(cfg)
		errMsg := ""
		if err != nil {
			errMsg = "Failed to load users: " + err.Error()
		}

		flash := ""
		if cookie, err := r.Cookie("flash"); err == nil {
			flash = cookie.Value
			http.SetCookie(w, &http.Cookie{
				Name:   "flash",
				Value:  "",
				Path:   "/",
				MaxAge: -1,
			})
		}

		data := usersData{
			Domain: cfg.Domain,
			Users:  users,
			Flash:  flash,
			Error:  errMsg,
		}
		renderTemplate(w, "users.html", data)
	}
}

// UsersAdd handles POST /users/add - adds a new email account.
func UsersAdd(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		if err := r.ParseForm(); err != nil {
			setFlash(w, "error:Bad request")
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		password := r.FormValue("password")
		confirm := r.FormValue("confirm_password")

		// Validate
		if email == "" {
			setFlash(w, "error:Email is required.")
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}
		if !strings.Contains(email, "@") {
			setFlash(w, "error:Invalid email address.")
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}
		if len(password) < 8 {
			setFlash(w, "error:Password must be at least 8 characters.")
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}
		if password != confirm {
			setFlash(w, "error:Passwords do not match.")
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		if err := mail.AddUser(email, password, cfg); err != nil {
			setFlash(w, "error:Failed to add user: "+err.Error())
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		db.LogAction("add_user", email, "", getIP(r))
		setFlash(w, "success:User "+email+" added successfully.")
		http.Redirect(w, r, "/users", http.StatusSeeOther)
	}
}

// UsersDelete handles POST /users/delete - removes an email account.
func UsersDelete(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		if err := r.ParseForm(); err != nil {
			setFlash(w, "error:Bad request")
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		email := strings.TrimSpace(r.FormValue("email"))
		if email == "" {
			setFlash(w, "error:Email is required.")
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		// Prevent deleting admin email
		if email == cfg.AdminEmail {
			setFlash(w, "error:Cannot delete the admin account.")
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		if err := mail.DeleteUser(email, cfg); err != nil {
			setFlash(w, "error:Failed to delete user: "+err.Error())
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		db.LogAction("delete_user", email, "", getIP(r))
		setFlash(w, "success:User "+email+" deleted.")
		http.Redirect(w, r, "/users", http.StatusSeeOther)
	}
}

// setFlash stores a flash message in a cookie (format: "type:message").
func setFlash(w http.ResponseWriter, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "flash",
		Value:    message,
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
