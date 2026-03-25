package handlers

import (
	"net/http"
	"os"
	"strings"

	"mailserver/config"
)

type dnsRecord struct {
	Type  string
	Name  string
	Value string
	TTL   string
	Note  string
}

type dnsData struct {
	Domain      string
	MailHost    string
	ServerIP    string
	DNSRecords  []dnsRecord
	DKIMKey     string
	DKIMFound   bool
}

// DNS handles GET /dns - shows the DNS records needed for the mail server.
func DNS(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mailHost := "mail." + cfg.Domain
		serverIP := getServerIP()
		dkimKey, dkimFound := loadDKIMPublicKey(cfg.Domain)

		records := []dnsRecord{
			{
				Type:  "A",
				Name:  "mail",
				Value: serverIP,
				TTL:   "3600",
				Note:  "Points mail.yourdomain.com to your server's IP address",
			},
			{
				Type:  "MX",
				Name:  "@",
				Value: mailHost + " (priority 10)",
				TTL:   "3600",
				Note:  "Tells the internet to deliver email to your mail server",
			},
			{
				Type:  "TXT",
				Name:  "@",
				Value: "v=spf1 mx a:" + mailHost + " ~all",
				TTL:   "3600",
				Note:  "SPF record: authorizes your server to send mail for this domain",
			},
			{
				Type:  "TXT",
				Name:  "mail._domainkey",
				Value: "v=DKIM1; k=rsa; p=" + dkimKey,
				TTL:   "3600",
				Note:  "DKIM public key: allows receivers to verify your email signatures",
			},
			{
				Type:  "TXT",
				Name:  "_dmarc",
				Value: "v=DMARC1; p=quarantine; rua=mailto:postmaster@" + cfg.Domain + "; ruf=mailto:postmaster@" + cfg.Domain + "; fo=1",
				TTL:   "3600",
				Note:  "DMARC policy: tells receivers what to do with unauthenticated mail",
			},
		}

		data := dnsData{
			Domain:     cfg.Domain,
			MailHost:   mailHost,
			ServerIP:   serverIP,
			DNSRecords: records,
			DKIMKey:    dkimKey,
			DKIMFound:  dkimFound,
		}

		renderTemplate(w, "dns.html", data)
	}
}

// getServerIP tries to determine the server's public IP address.
func getServerIP() string {
	// Try reading from a cached file first
	if data, err := os.ReadFile("/var/lib/mailserver/server_ip.txt"); err == nil {
		ip := strings.TrimSpace(string(data))
		if ip != "" {
			return ip
		}
	}

	// Try common methods
	methods := []struct {
		url string
	}{
		{"https://ifconfig.me"},
		{"https://api.ipify.org"},
		{"https://icanhazip.com"},
	}

	for _, m := range methods {
		_ = m // HTTP calls avoided in handlers - would need context/timeout
	}

	return "<your-server-ip>"
}

// loadDKIMPublicKey reads the DKIM public key for the domain.
func loadDKIMPublicKey(domain string) (string, bool) {
	keyFile := "/etc/opendkim/keys/" + domain + "/mail.txt"
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return "<DKIM key not found - run installer>", false
	}

	// The file looks like:
	// mail._domainkey	IN	TXT	( "v=DKIM1; k=rsa; "
	//   "p=MIIBIjANBgk..." )
	content := string(data)

	// Extract the p= value
	var keyParts []string
	inQuote := false
	var current strings.Builder

	for _, ch := range content {
		switch ch {
		case '"':
			if inQuote {
				keyParts = append(keyParts, current.String())
				current.Reset()
				inQuote = false
			} else {
				inQuote = true
			}
		default:
			if inQuote {
				current.WriteRune(ch)
			}
		}
	}

	// Join all quoted parts and extract p= value
	fullKey := strings.Join(keyParts, "")
	if idx := strings.Index(fullKey, "p="); idx != -1 {
		pValue := fullKey[idx+2:]
		// Take until semicolon or end
		if end := strings.Index(pValue, ";"); end != -1 {
			pValue = pValue[:end]
		}
		pValue = strings.TrimSpace(pValue)
		if pValue != "" {
			return pValue, true
		}
	}

	// Fallback: return the whole thing stripped of whitespace
	result := strings.Join(keyParts, "")
	result = strings.ReplaceAll(result, " ", "")
	return result, true
}
