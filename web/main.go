package main

import (
	"golang.org/x/crypto/bcrypt"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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
	log.Printf("User portal at https://%s", cfg.PortalHostname)

	// Static files directory
	staticDir := "/opt/mailserver/web/static"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		// Fallback to local directory for development
		staticDir = "./static"
	}

	// Admin mux — existing admin panel routes
	adminMux := http.NewServeMux()
	adminMux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	adminMux.HandleFunc("/login", handlers.Login(cfg))
	adminMux.HandleFunc("/logout", handlers.Logout(cfg))
	auth := middleware.RequireAuth(cfg)
	adminMux.Handle("/", auth(handlers.Dashboard(cfg)))
	adminMux.Handle("/users", auth(handlers.Users(cfg)))
	adminMux.Handle("/users/add", auth(handlers.UsersAdd(cfg)))
	adminMux.Handle("/users/delete", auth(handlers.UsersDelete(cfg)))
	adminMux.Handle("/emails", auth(handlers.Emails(cfg)))
	adminMux.Handle("/credentials", auth(handlers.Credentials(cfg)))
	adminMux.Handle("/dns", auth(handlers.DNS(cfg)))

	// Portal mux — user webmail routes (no /portal/ prefix)
	portalMux := http.NewServeMux()
	portalMux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	portalMux.HandleFunc("/login", handlers.PortalLogin(cfg))
	portalMux.HandleFunc("/logout", handlers.PortalLogout(cfg))
	portalMux.HandleFunc("/", handlers.PortalHandler(cfg))

	// Top-level router: dispatch by Host header
	portalHost := cfg.PortalHostname
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.LastIndex(host, ":"); i != -1 {
			host = host[:i]
		}
if host == portalHost {
			portalMux.ServeHTTP(w, r)
		} else {
			adminMux.ServeHTTP(w, r)
		}
	})

	// HTTP -> HTTPS redirect + ACME challenge handler
	certManager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.Hostname, cfg.PortalHostname),
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
		Handler:      router,
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
