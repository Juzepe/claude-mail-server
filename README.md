# Claude Mail Server

A lightweight, self-hosted mail server with a web admin panel. Built to run on a $4–6/mo DigitalOcean droplet (512MB RAM minimum). One command to install.

**Stack:** Postfix · Dovecot · OpenDKIM · Go web UI · SQLite · Let's Encrypt

---

## Features

- **One-command installer** — runs on any fresh Ubuntu/Debian server
- **Web admin panel** at `https://mail.yourdomain.com` — separate from your main website
- **Virtual mailboxes** — create and delete email accounts via the UI or CLI
- **Email browser** — view sent and received emails per account directly in the panel
- **App credentials page** — ready-to-paste config for Laravel, PHPMailer, Nodemailer
- **DNS setup guide** — shows your exact SPF, DKIM, DMARC, and MX records
- **Auto-renewing TLS** via Let's Encrypt (no nginx required)
- **DKIM signing** on all outgoing mail via OpenDKIM
- **SASL auth** — Postfix authenticates through Dovecot
- Enforces TLS 1.2+, bcrypt admin passwords, secure session cookies

---

## Requirements

- Ubuntu 20.04 / 22.04 / 24.04 or Debian 11 / 12
- A domain name with DNS control
- A VPS with a **dedicated IP** and **port 25 open** (DigitalOcean requires a support request to unblock port 25)
- Root access

**Minimum specs:** 512 MB RAM · 1 vCPU · 20 GB SSD

---

## Install

```bash
sudo git clone https://github.com/Juzepe/claude-mail-server.git /opt/mailserver
cd /opt/mailserver
sudo bash install.sh
```

The installer will ask for:

- **Mail domain** — e.g. `example.com` (this becomes your email domain: `you@example.com`)
- **Admin email** — e.g. `admin@example.com`
- **Admin password** — minimum 8 characters

When it finishes, it prints all DNS records you need to add and the URL to your admin panel.

---

## DNS Setup

After installation, add these records at your DNS provider:

| Type | Name              | Value                                                              |
|------|-------------------|--------------------------------------------------------------------|
| A    | `mail`            | `<your server IP>`                                                 |
| MX   | `@`               | `mail.yourdomain.com` (priority 10)                                |
| TXT  | `@`               | `v=spf1 mx a:mail.yourdomain.com ~all`                             |
| TXT  | `mail._domainkey` | `v=DKIM1; k=rsa; p=<key>` (shown in admin panel → DNS Setup)      |
| TXT  | `_dmarc`          | `v=DMARC1; p=quarantine; rua=mailto:postmaster@yourdomain.com`     |

Your exact DKIM public key is shown in the admin panel under **DNS Setup** after installation.

> **PTR record:** Ask your hosting provider to set a reverse DNS (PTR) record for your server IP pointing to `mail.yourdomain.com`. This is important for avoiding spam filters.

---

## Admin Panel

The admin panel runs at **`https://mail.yourdomain.com`** — a subdomain, so your main website at `yourdomain.com` is unaffected.

| Page             | Description                                              |
|------------------|----------------------------------------------------------|
| Dashboard        | Service status, disk usage, recent activity              |
| Email Accounts   | Create and delete mailboxes                              |
| Browse Emails    | View inbox and sent mail per account                     |
| App Credentials  | Copy-paste SMTP/IMAP config for apps                     |
| DNS Setup        | Your exact DNS records including DKIM public key         |

---

## Using with Laravel

Add to your `.env`:

```env
MAIL_MAILER=smtp
MAIL_HOST=mail.yourdomain.com
MAIL_PORT=587
MAIL_USERNAME=noreply@yourdomain.com
MAIL_PASSWORD=your_password
MAIL_ENCRYPTION=tls
MAIL_FROM_ADDRESS=noreply@yourdomain.com
MAIL_FROM_NAME="${APP_NAME}"
```

---

## Using with PHP (PHPMailer)

```php
$mail = new PHPMailer(true);
$mail->isSMTP();
$mail->Host       = 'mail.yourdomain.com';
$mail->SMTPAuth   = true;
$mail->Username   = 'noreply@yourdomain.com';
$mail->Password   = 'your_password';
$mail->SMTPSecure = PHPMailer::ENCRYPTION_STARTTLS;
$mail->Port       = 587;
```

---

## Using with Node.js (Nodemailer)

```javascript
const transporter = nodemailer.createTransport({
  host: 'mail.yourdomain.com',
  port: 587,
  secure: false, // STARTTLS
  auth: { user: 'noreply@yourdomain.com', pass: 'your_password' }
});
```

---

## Connection Settings (Mail Clients)

| Setting       | Value                         |
|---------------|-------------------------------|
| SMTP Host     | `mail.yourdomain.com`         |
| SMTP Port     | `587` (STARTTLS)              |
| SMTP Port     | `465` (SSL/TLS)               |
| IMAP Host     | `mail.yourdomain.com`         |
| IMAP Port     | `993` (SSL/TLS)               |
| POP3 Port     | `995` (SSL/TLS)               |
| Username      | Full email address            |
| Auth          | Normal password               |

---

## Managing Accounts via CLI

```bash
# Add a mailbox
mailserver-web -adduser user@example.com secretpassword

# Check service status
make status

# View logs
make logs
journalctl -u postfix -f
journalctl -u dovecot -f

# Renew SSL certificate (runs automatically, but can be forced)
certbot renew --force-renewal

# Rebuild and reinstall web UI after changes
make install

# Restart all services
systemctl restart postfix dovecot opendkim mailserver-web
```

---

## Project Structure

```
mailserver/
├── install.sh                  # One-command installer
├── Makefile
├── configs/
│   ├── postfix-main.cf.template
│   ├── postfix-master.cf
│   ├── dovecot.conf.template
│   └── opendkim.conf.template
├── systemd/
│   └── mailserver-web.service
└── web/                        # Go web application
    ├── main.go
    ├── go.mod
    ├── config/config.go
    ├── db/db.go
    ├── mail/manager.go
    ├── handlers/
    ├── middleware/
    ├── templates/
    └── static/
```

---

## Security

- Admin panel accessible over HTTPS only; HTTP redirects to HTTPS
- Sessions expire after 24 hours
- Admin password stored as bcrypt hash
- TLS 1.2+ enforced on SMTP and IMAP
- Mail data owned by `vmail` user (uid 5000) — no unnecessary root processes
- DKIM signing prevents spoofing
- DMARC set to `p=quarantine` by default — change to `p=reject` once delivery is confirmed working
- Postfix rate limiting enabled to prevent abuse
- `ufw` firewall rules applied during install

**Recommended after install:**
- Use a strong admin password (16+ chars)
- Switch DMARC to `p=reject` after verifying mail delivery works
- Install `fail2ban` for SSH and mail port brute-force protection
- Enable `unattended-upgrades` for automatic security patches

---

## Troubleshooting

**Mail not being received**
```bash
dig MX yourdomain.com          # Check MX record
systemctl status postfix       # Check service
tail -f /var/log/mail.log      # Check logs
```

**Mail going to spam**
- Verify SPF, DKIM, and DMARC records are all set correctly
- Test your score at https://mail-tester.com
- Ensure your PTR (reverse DNS) record matches `mail.yourdomain.com`

**TLS errors**
```bash
openssl s_client -connect mail.yourdomain.com:587 -starttls smtp
certbot renew --force-renewal
```

**Authentication failed**
```bash
make show-users                            # Verify account exists
journalctl -u dovecot | grep auth         # Check auth logs
```

---

## License

MIT
