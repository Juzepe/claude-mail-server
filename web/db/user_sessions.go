package db

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// UserSession holds an authenticated portal user's credentials and expiry.
type UserSession struct {
	Email     string
	Password  string
	ExpiresAt time.Time
}

var (
	userSessions   = map[string]*UserSession{}
	userSessionsMu sync.RWMutex
)

// CreateUserSession generates a new 32-byte hex session token, stores it with
// the given email/password, and returns the token. Sessions expire after 24h.
func CreateUserSession(email, password string) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)

	userSessionsMu.Lock()
	userSessions[token] = &UserSession{
		Email:     email,
		Password:  password,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	userSessionsMu.Unlock()

	return token, nil
}

// GetUserSession returns the session for the given token, or nil if it doesn't
// exist or has expired.
func GetUserSession(token string) (*UserSession, bool) {
	if token == "" {
		return nil, false
	}

	userSessionsMu.RLock()
	sess, ok := userSessions[token]
	userSessionsMu.RUnlock()

	if !ok {
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		DeleteUserSession(token)
		return nil, false
	}
	return sess, true
}

// DeleteUserSession removes the session for the given token from the store.
func DeleteUserSession(token string) {
	userSessionsMu.Lock()
	delete(userSessions, token)
	userSessionsMu.Unlock()
}
