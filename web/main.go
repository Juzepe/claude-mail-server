package main

import (
	"golang.org/x/crypto/bcrypt"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"mailserver/config"
	"mailserver/db"
	"mailserver/handlers"
	"mailserver/mail"
	"mailserver/middleware"

	"golang.org/x/crypto/acme/autocert"
)

func main() {
	// CLI flags for utility operations called from installer
	hashpw := flag.String("hashpw", "", "Hash a password with bcrypt and print to stdout")
	adduser := flag.String("adduser", "", "Add a mail user: -adduser email@domain password")
	flag.Parse()

	if *hashpw != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*hashpw), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error hashing password: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(hash))
		return
	}

	if *adduser != "" {
		args := flag.Args()
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "Usage: -adduser email password")
			os.Exit(1)
		}
		cfg := config.Load()
		if err := mail.AddUser(*adduser, args[0], cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error adding user: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("User %s added successfully\n", *adduser)
		return
	}

	// Normal server mode
	cfg := config.Load()

	if err := db.Init(cfg); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	log.Printf("Starting mailserver web UI at https://%s", cfg.Hostname)

	mux := http.NewServeMux()

	// Static files
	staticDir := "/opt/mailserver/web/static"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		// Fallback to local directory for development
		staticDir = "./static"
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))

	// Public routes
	mux.HandleFunc("/login", handlers.Login(cfg))
	mux.HandleFunc("/logout", handlers.Logout(cfg))

	// Protected routes
	auth := middleware.RequireAuth(cfg)
	mux.Handle("/", auth(handlers.Dashboard(cfg)))
	mux.Handle("/users", auth(handlers.Users(cfg)))
	mux.Handle("/users/add", auth(handlers.UsersAdd(cfg)))
	mux.Handle("/users/delete", auth(handlers.UsersDelete(cfg)))
	mux.Handle("/emails", auth(handlers.Emails(cfg)))
	mux.Handle("/credentials", auth(handlers.Credentials(cfg)))
	mux.Handle("/dns", auth(handlers.DNS(cfg)))

	// HTTP -> HTTPS redirect + ACME challenge handler
	certManager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.Hostname),
		Cache:      autocert.DirCache(cfg.DataDir + "/certs"),
		Email:      cfg.AdminEmail,
	}

	httpServer := &http.Server{
		Addr:         ":80",
		Handler:      certManager.HTTPHandler(redirectHTTPS()),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	httpsServer := &http.Server{
		Addr:         ":443",
		Handler:      mux,
		TLSConfig:    certManager.TLSConfig(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start HTTP server in background
	go func() {
		log.Println("HTTP server listening on :80 (redirect to HTTPS)")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	log.Println("HTTPS server listening on :443")
	if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTPS server error: %v", err)
	}
}

func redirectHTTPS() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}
