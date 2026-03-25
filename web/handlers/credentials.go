package handlers

import (
	"net/http"

	"mailserver/config"
	"mailserver/mail"
)

type credentialsData struct {
	Domain       string
	SMTPHost     string
	SMTPPort     string
	IMAPHost     string
	IMAPPort     string
	Users        []mail.MailUser
	LaravelEnv   string
	PHPExample   string
	NodeExample  string
}

// Credentials handles GET /credentials - shows connection info for mail clients and apps.
func Credentials(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := mail.ListUsers(cfg)
		if err != nil {
			users = []mail.MailUser{}
		}

		smtpHost := "mail." + cfg.Domain
		imapHost := "mail." + cfg.Domain

		laravelEnv := `MAIL_MAILER=smtp
MAIL_HOST=` + smtpHost + `
MAIL_PORT=587
MAIL_USERNAME=your@` + cfg.Domain + `
MAIL_PASSWORD=your_password
MAIL_ENCRYPTION=tls
MAIL_FROM_ADDRESS=your@` + cfg.Domain + `
MAIL_FROM_NAME="${APP_NAME}"`

		phpExample := `<?php
// Using PHPMailer
use PHPMailer\PHPMailer\PHPMailer;
use PHPMailer\PHPMailer\SMTP;

$mail = new PHPMailer(true);
$mail->isSMTP();
$mail->Host       = '` + smtpHost + `';
$mail->SMTPAuth   = true;
$mail->Username   = 'your@` + cfg.Domain + `';
$mail->Password   = 'your_password';
$mail->SMTPSecure = PHPMailer::ENCRYPTION_STARTTLS;
$mail->Port       = 587;

$mail->setFrom('your@` + cfg.Domain + `', 'Your Name');
$mail->addAddress('recipient@example.com');
$mail->Subject = 'Test Email';
$mail->Body    = 'Hello World!';
$mail->send();
?>`

		nodeExample := `// Using Nodemailer
const nodemailer = require('nodemailer');

const transporter = nodemailer.createTransport({
  host: '` + smtpHost + `',
  port: 587,
  secure: false, // STARTTLS
  auth: {
    user: 'your@` + cfg.Domain + `',
    pass: 'your_password'
  }
});

await transporter.sendMail({
  from: '"Your Name" <your@` + cfg.Domain + `>',
  to: 'recipient@example.com',
  subject: 'Test Email',
  text: 'Hello World!'
});`

		data := credentialsData{
			Domain:      cfg.Domain,
			SMTPHost:    smtpHost,
			SMTPPort:    "587",
			IMAPHost:    imapHost,
			IMAPPort:    "993",
			Users:       users,
			LaravelEnv:  laravelEnv,
			PHPExample:  phpExample,
			NodeExample: nodeExample,
		}

		renderTemplate(w, "credentials.html", data)
	}
}
