package middleware

import (
	"net/http"

	"mailserver/config"
	"mailserver/db"
)

const sessionCookieName = "mailserver_session"

// RequireAuth returns a middleware that enforces authentication.
// Unauthenticated requests are redirected to /login.
func RequireAuth(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil {
				redirectToLogin(w, r)
				return
			}

			if !db.ValidateSession(cookie.Value) {
				// Clear invalid cookie
				http.SetCookie(w, &http.Cookie{
					Name:   sessionCookieName,
					Value:  "",
					Path:   "/",
					MaxAge: -1,
				})
				redirectToLogin(w, r)
				return
			}

			// Valid session - proceed
			next.ServeHTTP(w, r)
		})
	}
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// Preserve the original URL for redirect after login
	target := "/login"
	if r.URL.Path != "/" && r.URL.Path != "" {
		target = "/login?next=" + r.URL.Path
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
