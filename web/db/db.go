package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	"mailserver/config"

	_ "modernc.org/sqlite"
)

var database *sql.DB

// Init opens the SQLite database and creates tables if needed.
func Init(cfg *config.Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0750); err != nil {
		return fmt.Errorf("failed to create data directory %s: %w", cfg.DataDir, err)
	}

	dbPath := cfg.DataDir + "/mailserver.db"
	var err error
	database, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err = database.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("Warning: could not set WAL mode: %v", err)
	}
	if _, err = database.Exec("PRAGMA foreign_keys=ON"); err != nil {
		log.Printf("Warning: could not enable foreign keys: %v", err)
	}

	if err = createTables(); err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}

	// Start background cleanup goroutine
	go cleanupExpiredSessions()

	log.Printf("Database initialized: %s", dbPath)
	return nil
}

// Close closes the database connection.
func Close() {
	if database != nil {
		database.Close()
	}
}

// DB returns the underlying database connection for use by other packages.
func DB() *sql.DB {
	return database
}

func createTables() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS admin_sessions (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			token      TEXT    NOT NULL UNIQUE,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_token ON admin_sessions(token)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON admin_sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			action    TEXT    NOT NULL,
			target    TEXT    NOT NULL DEFAULT '',
			detail    TEXT    NOT NULL DEFAULT '',
			ip_addr   TEXT    NOT NULL DEFAULT '',
			timestamp INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_log(timestamp)`,
	}

	for _, q := range queries {
		if _, err := database.Exec(q); err != nil {
			return fmt.Errorf("query error: %w\nQuery: %s", err, q)
		}
	}
	return nil
}

// Session represents an authenticated admin session.
type Session struct {
	ID        int64
	Token     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateSession generates a new session token and stores it in the DB.
// Sessions last 24 hours.
func CreateSession() (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	now := time.Now()
	expiresAt := now.Add(24 * time.Hour)

	_, err := database.Exec(
		`INSERT INTO admin_sessions (token, created_at, expires_at) VALUES (?, ?, ?)`,
		token, now.Unix(), expiresAt.Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	return token, nil
}

// ValidateSession checks if a token is valid and not expired.
// Returns true if valid, false otherwise.
func ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	var expiresAt int64
	err := database.QueryRow(
		`SELECT expires_at FROM admin_sessions WHERE token = ? AND expires_at > ?`,
		token, time.Now().Unix(),
	).Scan(&expiresAt)
	return err == nil
}

// DeleteSession removes a session token from the DB.
func DeleteSession(token string) error {
	_, err := database.Exec(`DELETE FROM admin_sessions WHERE token = ?`, token)
	return err
}

// AuditEntry represents a single audit log record.
type AuditEntry struct {
	ID        int64
	Action    string
	Target    string
	Detail    string
	IPAddr    string
	Timestamp time.Time
}

// LogAction records an action in the audit log.
func LogAction(action, target, detail, ipAddr string) {
	_, err := database.Exec(
		`INSERT INTO audit_log (action, target, detail, ip_addr, timestamp) VALUES (?, ?, ?, ?, ?)`,
		action, target, detail, ipAddr, time.Now().Unix(),
	)
	if err != nil {
		log.Printf("Warning: failed to write audit log: %v", err)
	}
}

// GetRecentAuditLog returns the most recent N audit log entries.
func GetRecentAuditLog(limit int) ([]AuditEntry, error) {
	rows, err := database.Query(
		`SELECT id, action, target, detail, ip_addr, timestamp
		 FROM audit_log
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &e.Action, &e.Target, &e.Detail, &e.IPAddr, &ts); err != nil {
			continue
		}
		e.Timestamp = time.Unix(ts, 0)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// cleanupExpiredSessions runs periodically to remove old sessions.
func cleanupExpiredSessions() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		result, err := database.Exec(
			`DELETE FROM admin_sessions WHERE expires_at < ?`,
			time.Now().Unix(),
		)
		if err != nil {
			log.Printf("Session cleanup error: %v", err)
			continue
		}
		n, _ := result.RowsAffected()
		if n > 0 {
			log.Printf("Cleaned up %d expired sessions", n)
		}
	}
}
