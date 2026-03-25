package mail

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mailserver/config"
)

// MailUser represents a single virtual mail account.
type MailUser struct {
	Email   string
	Domain  string
	User    string
	Initial string // First letter of the local part, uppercased
}

// ListUsers reads /etc/dovecot/users and returns all configured mail accounts.
func ListUsers(cfg *config.Config) ([]MailUser, error) {
	f, err := os.Open(cfg.DovecotUsersFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []MailUser{}, nil
		}
		return nil, fmt.Errorf("failed to open users file: %w", err)
	}
	defer f.Close()

	var users []MailUser
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Format: email:{hash}:5000:5000::/var/mail/vhosts/domain/user::userdb_mail=...
		parts := strings.Split(line, ":")
		if len(parts) < 1 {
			continue
		}
		email := parts[0]
		if !strings.Contains(email, "@") {
			continue
		}
		atIdx := strings.LastIndex(email, "@")
		domain := email[atIdx+1:]
		user := email[:atIdx]
		initial := "?"
		if len(user) > 0 {
			initial = strings.ToUpper(string(user[0]))
		}
		users = append(users, MailUser{
			Email:   email,
			Domain:  domain,
			User:    user,
			Initial: initial,
		})
	}
	return users, scanner.Err()
}

// AddUser creates a new virtual mail account.
// It hashes the password, appends to dovecot users, postfix vmailbox,
// and creates the maildir on disk.
func AddUser(email, password string, cfg *config.Config) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") {
		return fmt.Errorf("invalid email address: %s", email)
	}

	// Check for duplicate
	users, err := ListUsers(cfg)
	if err != nil {
		return fmt.Errorf("failed to list users: %w", err)
	}
	for _, u := range users {
		if u.Email == email {
			return fmt.Errorf("user %s already exists", email)
		}
	}

	atIdx := strings.LastIndex(email, "@")
	domain := email[atIdx+1:]
	user := email[:atIdx]

	// Hash password using doveadm
	hash, err := hashPassword(password)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Dovecot users entry format:
	// email:{SHA512-CRYPT}hash:5000:5000::/var/mail/vhosts/domain/user::userdb_mail=maildir:/var/mail/vhosts/domain/user
	maildir := filepath.Join(cfg.MailDir, domain, user)
	dovecotEntry := fmt.Sprintf("%s:%s:5000:5000::%s::userdb_mail=maildir:%s\n",
		email, hash, maildir, maildir)

	if err := appendToFile(cfg.DovecotUsersFile, dovecotEntry); err != nil {
		return fmt.Errorf("failed to update dovecot users: %w", err)
	}

	// Postfix vmailbox entry format:
	// email  domain/user/
	vmailboxEntry := fmt.Sprintf("%s  %s/%s/\n", email, domain, user)
	if err := appendToFile(cfg.PostfixVmailboxFile, vmailboxEntry); err != nil {
		return fmt.Errorf("failed to update postfix vmailbox: %w", err)
	}

	// Rebuild postfix maps
	if err := postmap(cfg.PostfixVmailboxFile); err != nil {
		return fmt.Errorf("postmap failed: %w", err)
	}

	// Create maildir structure
	if err := createMaildir(maildir); err != nil {
		return fmt.Errorf("failed to create maildir: %w", err)
	}

	return nil
}

// DeleteUser removes a virtual mail account from all config files.
func DeleteUser(email string, cfg *config.Config) error {
	email = strings.ToLower(strings.TrimSpace(email))

	if err := removeLineFromFile(cfg.DovecotUsersFile, email); err != nil {
		return fmt.Errorf("failed to update dovecot users: %w", err)
	}

	if err := removeLineFromFile(cfg.PostfixVmailboxFile, email); err != nil {
		return fmt.Errorf("failed to update postfix vmailbox: %w", err)
	}

	if err := postmap(cfg.PostfixVmailboxFile); err != nil {
		return fmt.Errorf("postmap failed: %w", err)
	}

	return nil
}

// ChangePassword updates the password for an existing user.
func ChangePassword(email, newPassword string, cfg *config.Config) error {
	email = strings.ToLower(strings.TrimSpace(email))

	hash, err := hashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Read file, find user line, replace hash
	content, err := os.ReadFile(cfg.DovecotUsersFile)
	if err != nil {
		return fmt.Errorf("failed to read users file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, email+":") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) >= 3 {
				lines[i] = email + ":" + hash + ":" + parts[2]
				found = true
				break
			}
		}
	}

	if !found {
		return fmt.Errorf("user %s not found", email)
	}

	newContent := strings.Join(lines, "\n")
	return os.WriteFile(cfg.DovecotUsersFile, []byte(newContent), 0640)
}

// hashPassword uses doveadm to generate a SHA512-CRYPT hash suitable for Dovecot.
func hashPassword(password string) (string, error) {
	cmd := exec.Command("doveadm", "pw", "-s", "SHA512-CRYPT", "-p", password)
	output, err := cmd.Output()
	if err != nil {
		// Fallback: try openssl
		return hashPasswordOpenSSL(password)
	}
	hash := strings.TrimSpace(string(output))
	return hash, nil
}

func hashPasswordOpenSSL(password string) (string, error) {
	cmd := exec.Command("openssl", "passwd", "-6", password)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("both doveadm and openssl failed: %w", err)
	}
	hash := strings.TrimSpace(string(output))
	// Wrap in {SHA512-CRYPT} prefix for Dovecot
	return "{SHA512-CRYPT}" + hash, nil
}

// postmap runs postmap on the given file to rebuild the hash database.
func postmap(filePath string) error {
	cmd := exec.Command("postmap", filePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("postmap %s failed: %w\n%s", filePath, err, output)
	}
	return nil
}

// createMaildir creates the Maildir++ directory structure with proper ownership.
func createMaildir(maildir string) error {
	dirs := []string{
		maildir,
		filepath.Join(maildir, "new"),
		filepath.Join(maildir, "cur"),
		filepath.Join(maildir, "tmp"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return err
		}
		// chown to vmail (5000:5000)
		if err := os.Chown(dir, 5000, 5000); err != nil {
			// Non-fatal if running as non-root in dev
			_ = err
		}
	}
	return nil
}

// appendToFile appends content to a file, creating it if it doesn't exist.
func appendToFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// removeLineFromFile removes all lines starting with the given prefix from a file.
func removeLineFromFile(path, prefix string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(content), "\n")
	var kept []string
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix+":") && !strings.HasPrefix(line, prefix+" ") {
			kept = append(kept, line)
		}
	}

	newContent := strings.Join(kept, "\n")
	// Remove trailing blank lines but keep one newline at end
	newContent = strings.TrimRight(newContent, "\n") + "\n"

	return os.WriteFile(path, []byte(newContent), 0640)
}
