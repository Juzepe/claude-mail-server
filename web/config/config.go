package config

import (
	"bufio"
	"log"
	"os"
	"strings"
)

// Config holds all runtime configuration for the mail server web UI.
type Config struct {
	Domain              string
	Hostname            string // mail.domain.com — used for web UI and mail server TLS
	AdminEmail          string
	AdminPasswordHash   string
	DataDir             string
	MailDir             string
	DovecotUsersFile    string
	PostfixVmailboxFile string
}

const defaultConfigPath = "/etc/mailserver/config.env"

// Load reads configuration from /etc/mailserver/config.env.
// Falls back to environment variables if the file doesn't exist.
func Load() *Config {
	cfg := &Config{
		DataDir:             "/var/lib/mailserver",
		MailDir:             "/var/mail/vhosts",
		DovecotUsersFile:    "/etc/dovecot/users",
		PostfixVmailboxFile: "/etc/postfix/vmailbox",
	}

	// Try loading from file first
	configPath := os.Getenv("MAILSERVER_CONFIG")
	if configPath == "" {
		configPath = defaultConfigPath
	}

	if err := cfg.loadFile(configPath); err != nil {
		log.Printf("Warning: could not load config from %s: %v", configPath, err)
		log.Println("Falling back to environment variables.")
	}

	// Environment variables override file values
	cfg.applyEnv()

	// Validate required fields
	if cfg.Domain == "" {
		log.Fatal("Config error: DOMAIN is required.")
	}
	if cfg.AdminEmail == "" {
		log.Fatal("Config error: ADMIN_EMAIL is required.")
	}
	if cfg.AdminPasswordHash == "" {
		log.Fatal("Config error: ADMIN_PASSWORD_HASH is required.")
	}

	// Derive hostname from domain if not explicitly set
	if cfg.Hostname == "" {
		cfg.Hostname = "mail." + cfg.Domain
	}

	return cfg
}

func (c *Config) loadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		// Strip surrounding quotes if present
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		c.set(key, value)
	}
	return scanner.Err()
}

func (c *Config) applyEnv() {
	envVars := map[string]*string{
		"DOMAIN":                &c.Domain,
		"HOSTNAME":              &c.Hostname,
		"ADMIN_EMAIL":           &c.AdminEmail,
		"ADMIN_PASSWORD_HASH":   &c.AdminPasswordHash,
		"DATA_DIR":              &c.DataDir,
		"MAIL_DIR":              &c.MailDir,
		"DOVECOT_USERS_FILE":    &c.DovecotUsersFile,
		"POSTFIX_VMAILBOX_FILE": &c.PostfixVmailboxFile,
	}
	for key, ptr := range envVars {
		if v := os.Getenv(key); v != "" {
			*ptr = v
		}
	}
}

func (c *Config) set(key, value string) {
	switch key {
	case "DOMAIN":
		c.Domain = value
	case "HOSTNAME":
		c.Hostname = value
	case "ADMIN_EMAIL":
		c.AdminEmail = value
	case "ADMIN_PASSWORD_HASH":
		c.AdminPasswordHash = value
	case "DATA_DIR":
		c.DataDir = value
	case "MAIL_DIR":
		c.MailDir = value
	case "DOVECOT_USERS_FILE":
		c.DovecotUsersFile = value
	case "POSTFIX_VMAILBOX_FILE":
		c.PostfixVmailboxFile = value
	}
}
