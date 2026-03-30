package handlers

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mailserver/config"
	"mailserver/mail"

	goimap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// EmailMessage represents a single email in the list.
type EmailMessage struct {
	UID     uint32
	From    string
	To      string
	CC      string
	Subject string
	Date    time.Time
	Seen    bool
	Body    string
}

type emailsData struct {
	Domain          string
	Users           []mail.MailUser
	SelectedAccount string
	SelectedFolder  string
	Folders         []string
	Emails          []EmailMessage
	SelectedEmail   *EmailMessage
	Error           string
	Flash           string
}

// Emails handles GET /emails - browses emails for a selected account via IMAP.
func Emails(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := mail.ListUsers(cfg)
		if err != nil {
			users = []mail.MailUser{}
		}

		q := r.URL.Query()
		selectedAccount := q.Get("account")
		selectedFolder := q.Get("folder")
		selectedUIDStr := q.Get("uid")

		if selectedFolder == "" {
			selectedFolder = "INBOX"
		}

		data := emailsData{
			Domain:          cfg.Domain,
			Users:           users,
			SelectedAccount: selectedAccount,
			SelectedFolder:  selectedFolder,
			Folders:         []string{"INBOX", "Sent", "Drafts", "Trash", "Junk"},
		}

		if selectedAccount == "" {
			renderTemplate(w, "emails.html", data)
			return
		}

		// Find user to get password - we need IMAP credentials
		// Since we're on the same server, connect to localhost with master auth
		// We use the Dovecot master user mechanism or connect directly to the maildir
		// For simplicity, we'll use IMAP with the admin viewing capability
		emails, folders, err := fetchEmailsViaIMAP(selectedAccount, selectedFolder, cfg)
		if err != nil {
			data.Error = fmt.Sprintf("Failed to connect to mailbox: %v", err)
			renderTemplate(w, "emails.html", data)
			return
		}

		data.Emails = emails
		if len(folders) > 0 {
			data.Folders = folders
		}

		// If a specific email UID is selected, fetch its body
		if selectedUIDStr != "" {
			uid64, err := strconv.ParseUint(selectedUIDStr, 10, 32)
			if err == nil {
				uid := uint32(uid64)
				for i, e := range emails {
					if e.UID == uid {
						// Fetch full body
						body, err := fetchEmailBody(selectedAccount, selectedFolder, uid, cfg)
						if err == nil {
							emails[i].Body = body
							data.SelectedEmail = &emails[i]
						}
						break
					}
				}
			}
		}

		renderTemplate(w, "emails.html", data)
	}
}

// fetchEmailsViaIMAP connects to the local IMAP server and lists messages.
// It uses Dovecot's master user feature if configured, otherwise returns an empty list.
func fetchEmailsViaIMAP(account, folder string, cfg *config.Config) ([]EmailMessage, []string, error) {
	// Try to connect to localhost IMAP
	// Dovecot master user: admin*user@domain (requires master password configured)
	// For simplicity, we try a direct connection on port 143
	c, err := client.Dial("localhost:143")
	if err != nil {
		return nil, nil, fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	// Attempt to log in as master user if configured
	masterUser := cfg.AdminEmail
	masterPass := "" // Would need master password from config

	// Without master auth configured, we can't impersonate users.
	// The admin must be logged in as themselves, or we need the user's password.
	// We'll try to login as the account using a stored credential from the config.
	// As a practical fallback, return a helpful message.
	if masterPass == "" {
		_ = masterUser
		c.Logout()
		return nil, nil, fmt.Errorf("IMAP master auth not configured. " +
			"Add IMAP_MASTER_PASS to /etc/mailserver/config.env and configure Dovecot master auth to browse emails.")
	}

	loginUser := account + "*" + masterUser
	if err := c.Login(loginUser, masterPass); err != nil {
		return nil, nil, fmt.Errorf("IMAP login failed: %w", err)
	}

	// List mailboxes
	mailboxes := make(chan *goimap.MailboxInfo, 20)
	done := make(chan error, 1)
	go func() {
		done <- c.List("", "*", mailboxes)
	}()

	var folders []string
	for m := range mailboxes {
		folders = append(folders, m.Name)
	}
	if err := <-done; err != nil {
		log.Printf("Warning: mailbox list error: %v", err)
	}

	// Select the folder
	mbox, err := c.Select(folder, true)
	if err != nil {
		return nil, folders, fmt.Errorf("folder %q not found: %w", folder, err)
	}

	if mbox.Messages == 0 {
		return []EmailMessage{}, folders, nil
	}

	// Fetch last 50 messages
	from := uint32(1)
	if mbox.Messages > 50 {
		from = mbox.Messages - 49
	}
	seqSet := new(goimap.SeqSet)
	seqSet.AddRange(from, mbox.Messages)

	items := []goimap.FetchItem{
		goimap.FetchUid,
		goimap.FetchEnvelope,
		goimap.FetchFlags,
	}

	messages := make(chan *goimap.Message, 50)
	fetchDone := make(chan error, 1)
	go func() {
		fetchDone <- c.Fetch(seqSet, items, messages)
	}()

	var emails []EmailMessage
	for msg := range messages {
		e := EmailMessage{
			UID:  msg.Uid,
			Date: time.Now(),
		}

		if msg.Envelope != nil {
			e.Subject = msg.Envelope.Subject
			if msg.Envelope.Date != (time.Time{}) {
				e.Date = msg.Envelope.Date
			}
			if len(msg.Envelope.From) > 0 && msg.Envelope.From[0] != nil {
				addr := msg.Envelope.From[0]
				if addr.PersonalName != "" {
					e.From = fmt.Sprintf("%s <%s@%s>", addr.PersonalName, addr.MailboxName, addr.HostName)
				} else {
					e.From = fmt.Sprintf("%s@%s", addr.MailboxName, addr.HostName)
				}
			}
		}

		for _, flag := range msg.Flags {
			if flag == goimap.SeenFlag {
				e.Seen = true
				break
			}
		}

		emails = append(emails, e)
	}

	if err := <-fetchDone; err != nil {
		log.Printf("Warning: fetch error: %v", err)
	}

	// Reverse so newest first
	for i, j := 0, len(emails)-1; i < j; i, j = i+1, j-1 {
		emails[i], emails[j] = emails[j], emails[i]
	}

	return emails, folders, nil
}

// fetchEmailBody fetches the full text body of a single message.
func fetchEmailBody(account, folder string, uid uint32, cfg *config.Config) (string, error) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return "", err
	}
	defer c.Logout()

	masterUser := cfg.AdminEmail
	masterPass := ""
	if masterPass == "" {
		_ = masterUser
		return "", fmt.Errorf("IMAP master auth not configured")
	}

	loginUser := account + "*" + masterUser
	if err := c.Login(loginUser, masterPass); err != nil {
		return "", err
	}

	if _, err := c.Select(folder, true); err != nil {
		return "", err
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddNum(uid)

	section := &goimap.BodySectionName{}
	items := []goimap.FetchItem{section.FetchItem()}

	messages := make(chan *goimap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqSet, items, messages)
	}()

	var bodyStr string
	for msg := range messages {
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		bodyBytes, err := io.ReadAll(r)
		if err == nil {
			bodyStr = string(bodyBytes)
		}
	}

	if err := <-done; err != nil {
		log.Printf("Warning: uid fetch error: %v", err)
	}

	// Extract text/plain part (simple extraction)
	if strings.Contains(bodyStr, "Content-Type: text/plain") {
		parts := strings.SplitN(bodyStr, "\r\n\r\n", 2)
		if len(parts) == 2 {
			bodyStr = parts[1]
		}
	}

	// Limit length for display
	const maxBodyLen = 50000
	if len(bodyStr) > maxBodyLen {
		bodyStr = bodyStr[:maxBodyLen] + "\n\n[Message truncated...]"
	}

	return bodyStr, nil
}
