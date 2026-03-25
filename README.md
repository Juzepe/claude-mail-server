# Mail Server

A lightweight, self-hosted mail server with a web-based admin UI. Runs comfortably on a $4-6/mo DigitalOcean droplet (512MB-1GB RAM).

**Stack:** Postfix + Dovecot + OpenDKIM + Go web UI + SQLite + Let's Encrypt

---

## Features

- Single bash installer — run one command on a fresh Ubuntu server
- Web admin panel (dark theme) — create/delete email accounts, view DNS setup
- Postfix for SMTP with SASL auth and rate limiting
- Dovecot for IMAP/POP3 with virtual mailboxes (Maildir format)
- OpenDKIM for email signing (DKIM)
- Let's Encrypt SSL/TLS — auto-renewed
- SPF, DKIM, DMARC guidance
- App credential pages with Laravel, PHP, and Node.js examples

---

## Requirements

- Ubuntu 20.04, 22.04, or 24.04 (or Debian 11/12)
- A domain name with DNS control
- A VPS with a dedicated IP address
- Port 25 open (some providers block it — DigitalOcean allows it by request)
- Root access

**Minimum specs:** 512MB RAM, 1 vCPU, 20GB SSD

---

## Quick Install

```bash
# On a fresh Ubuntu 22.04 server, as root:
git clone https://github.com/yourusername/mailserver.git /opt/mailserver-src
cd /opt/mailserver-src
chmod +x install.sh
sudo ./install.sh
```

The installer will prompt you for:
- Your mail domain (e.g., `example.com`)
- Admin email address (e.g., `admin@example.com`)
- Admin panel password

After installation, visit `https://yourdomain.com` to access the admin panel.

---

## Post-Install DNS Setup

Add these DNS records at your registrar/DNS provider:

| Type | Name             | Value                                      |
|------|------------------|--------------------------------------------|
| A    | `mail`           | `<your server IP>`                         |
| MX   | `@`              | `mail.yourdomain.com` (priority 10)        |
| TXT  | `@`              | `v=spf1 mx a:mail.yourdomain.com ~all`     |
| TXT  | `mail._domainkey`| `v=DKIM1; k=rsa; p=<key from DNS page>`    |
| TXT  | `_dmarc`         | `v=DMARC1; p=quarantine; rua=mailto:postmaster@yourdomain.com` |

The exact values (including your DKIM public key) are shown in the admin panel under **DNS Setup**.

**Important:** Also ask your hosting provider to set a **PTR (reverse DNS)** record for your server IP pointing to `mail.yourdomain.com`. This is critical for avoiding spam classification.

---

## Managing Email Accounts

### Via the Web UI

1. Go to `https://yourdomain.com`
2. Log in with your admin email and password
3. Click **Email Accounts**
4. Fill in the email address and password, click **Create Account**

### Via command line

```bash
# Add a user
mailserver-web -adduser user@example.com secretpassword

# Or use make
make show-users
```

---

## Using with Laravel

Add to your `.env`:

```env
MAIL_MAILER=smtp
MAIL_HOST=mail.yourdomain.com
MAIL_PORT=587
MAIL_USERNAME=noreply@yourdomain.com
MAIL_PASSWORD=your_email_password
MAIL_ENCRYPTION=tls
MAIL_FROM_ADDRESS=noreply@yourdomain.com
MAIL_FROM_NAME="${APP_NAME}"
```

---

## Using with PHP (PHPMailer)

```php
<?php
use PHPMailer\PHPMailer\PHPMailer;

$mail = new PHPMailer(true);
$mail->isSMTP();
$mail->Host       = 'mail.yourdomain.com';
$mail->SMTPAuth   = true;
$mail->Username   = 'noreply@yourdomain.com';
$mail->Password   = 'your_password';
$mail->SMTPSecure = PHPMailer::ENCRYPTION_STARTTLS;
$mail->Port       = 587;

$mail->setFrom('noreply@yourdomain.com', 'Your App');
$mail->addAddress('recipient@example.com');
$mail->Subject = 'Hello';
$mail->Body    = 'Test email from my server.';
$mail->send();
```

---

## Using with Node.js (Nodemailer)

```javascript
const nodemailer = require('nodemailer');

const transporter = nodemailer.createTransport({
  host: 'mail.yourdomain.com',
  port: 587,
  secure: false,
  auth: {
    user: 'noreply@yourdomain.com',
    pass: 'your_password'
  }
});

await transporter.sendMail({
  from: '"My App" <noreply@yourdomain.com>',
  to: 'recipient@example.com',
  subject: 'Hello',
  text: 'Test email'
});
```

---

## Connection Settings (Mail Clients)

| Setting          | Value                    |
|------------------|--------------------------|
| SMTP Host        | `mail.yourdomain.com`    |
| SMTP Port        | `587` (STARTTLS)         |
| SMTP Alt Port    | `465` (SSL/TLS)          |
| IMAP Host        | `mail.yourdomain.com`    |
| IMAP Port        | `993` (SSL/TLS)          |
| POP3 Port        | `995` (SSL/TLS)          |
| Username         | Full email address       |
| Authentication   | Normal password          |

---

## Project Structure

```
mailserver/
├── install.sh              # One-command installer
├── Makefile                # Build targets
├── configs/
│   ├── postfix-main.cf.template
│   ├── postfix-master.cf
│   ├── dovecot.conf.template
│   └── opendkim.conf.template
├── systemd/
│   └── mailserver-web.service
└── web/                    # Go web application
    ├── main.go
    ├── go.mod
    ├── config/config.go
    ├── db/db.go
    ├── mail/manager.go
    ├── handlers/
    │   ├── auth.go
    │   ├── dashboard.go
    │   ├── users.go
    │   ├── emails.go
    │   ├── credentials.go
    │   └── dns.go
    ├── middleware/auth.go
    ├── templates/
    │   ├── layout.html
    │   ├── login.html
    │   ├── dashboard.html
    │   ├── users.html
    │   ├── emails.html
    │   ├── credentials.html
    │   └── dns.html
    └── static/
        ├── style.css
        └── app.js
```

---

## Maintenance

```bash
# Check service status
make status

# View logs
make logs
journalctl -u postfix -f
journalctl -u dovecot -f

# Renew SSL certificate (runs automatically, but manual trigger):
make cert-renew

# Rebuild and reinstall web UI
make install

# Restart all services
systemctl restart postfix dovecot opendkim mailserver-web
```

---

## Security Notes

- The admin panel is accessible over HTTPS only. HTTP redirects to HTTPS.
- Sessions expire after 24 hours.
- Passwords are stored as bcrypt hashes.
- TLS 1.2+ is enforced for all connections (SMTP, IMAP).
- The `vmail` user (uid 5000) owns all mail data. No process runs as root unnecessarily.
- DKIM signing prevents email spoofing.
- DMARC quarantine policy is set by default — change to `p=reject` once you've confirmed delivery is working.
- Rate limiting is enabled in Postfix to prevent abuse.
- Firewall rules are applied via `ufw` during installation.

**Change default settings for production:**
- Set a strong admin password (min 16 chars)
- After verifying mail is working, change DMARC policy to `p=reject`
- Consider adding fail2ban for SSH and mail port brute-force protection
- Enable unattended-upgrades for automatic security patches

---

## Troubleshooting

**Mail not being received:**
- Check MX record is set correctly: `dig MX yourdomain.com`
- Check postfix is running: `systemctl status postfix`
- Check logs: `tail -f /var/log/mail.log`

**Mail going to spam:**
- Verify SPF, DKIM, DMARC records are all set
- Test at https://mail-tester.com
- Ensure PTR record matches your hostname

**TLS errors:**
- Check certificate: `openssl s_client -connect mail.yourdomain.com:587 -starttls smtp`
- Renew cert: `certbot renew --force-renewal`

**Can't send email (authentication failed):**
- Verify the email account exists: `make show-users`
- Check Dovecot auth logs: `journalctl -u dovecot | grep auth`

---

## License

MIT License. See [LICENSE](LICENSE) for details.
