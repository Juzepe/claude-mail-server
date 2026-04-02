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

// EmailMessage represents a single email in the admin email browser.
type EmailMessage struct {
	UID     uint32
	Folder  string
	From    string
	To      string
	CC      string
	Subject string
	Date    time.Time
	Seen    bool
	Flagged bool
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

		if selectedUIDStr != "" {
			uid64, err := strconv.ParseUint(selectedUIDStr, 10, 32)
			if err == nil {
				uid := uint32(uid64)
				for i, e := range emails {
					if e.UID == uid {
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

func fetchEmailsViaIMAP(account, folder string, cfg *config.Config) ([]EmailMessage, []string, error) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return nil, nil, fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer c.Logout()

	masterUser := cfg.AdminEmail
	masterPass := ""
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

	mbox, err := c.Select(folder, true)
	if err != nil {
		return nil, folders, fmt.Errorf("folder %q not found: %w", folder, err)
	}

	if mbox.Messages == 0 {
		return []EmailMessage{}, folders, nil
	}

	from := uint32(1)
	if mbox.Messages > 50 {
		from = mbox.Messages - 49
	}
	seqSet := new(goimap.SeqSet)
	seqSet.AddRange(from, mbox.Messages)

	items := []goimap.FetchItem{goimap.FetchUid, goimap.FetchEnvelope, goimap.FetchFlags}
	messages := make(chan *goimap.Message, 50)
	fetchDone := make(chan error, 1)
	go func() {
		fetchDone <- c.Fetch(seqSet, items, messages)
	}()

	var emails []EmailMessage
	for msg := range messages {
		e := EmailMessage{UID: msg.Uid, Date: time.Now()}
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

	for i, j := 0, len(emails)-1; i < j; i, j = i+1, j-1 {
		emails[i], emails[j] = emails[j], emails[i]
	}

	return emails, folders, nil
}

func fetchEmailBody(account, folder string, uid uint32, cfg *config.Config) (string, error) {
	c, err := client.Dial("localhost:143")
	if err != nil {
		return "", err
	}
	defer c.Logout()

	masterPass := ""
	if masterPass == "" {
		return "", fmt.Errorf("IMAP master auth not configured")
	}

	loginUser := account + "*" + cfg.AdminEmail
	if err := c.Login(loginUser, masterPass); err != nil {
		return "", err
	}

	if _, err := c.Select(folder, true); err != nil {
		return "", err
	}

	seqSet := new(goimap.SeqSet)
	seqSet.AddNum(uid)

	section := &goimap.BodySectionName{}
	messages := make(chan *goimap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqSet, []goimap.FetchItem{section.FetchItem()}, messages)
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
	<-done

	if strings.Contains(bodyStr, "Content-Type: text/plain") {
		parts := strings.SplitN(bodyStr, "\r\n\r\n", 2)
		if len(parts) == 2 {
			bodyStr = parts[1]
		}
	}

	const maxBodyLen = 50000
	if len(bodyStr) > maxBodyLen {
		bodyStr = bodyStr[:maxBodyLen] + "\n\n[Message truncated...]"
	}

	return bodyStr, nil
}
