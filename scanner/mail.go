package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"net/smtp"
	"strings"
	"time"
)

// Mail is one email with a CSV attached.
type Mail struct {
	To       string
	Subject  string
	Body     string
	Filename string
	CSV      []byte
}

// Mailer sends mail. The server uses SMTP; tests swap in a fake.
type Mailer func(Mail) error

// smtpMailer sends through an SMTP server with STARTTLS (Gmail: an app
// password on smtp.gmail.com:587).
func smtpMailer(addr, user, password, replyTo string) Mailer {
	host, _, _ := strings.Cut(addr, ":")
	auth := smtp.PlainAuth("", user, password, host)
	return func(m Mail) error {
		return smtp.SendMail(addr, auth, user, []string{m.To}, buildMessage(user, replyTo, m))
	}
}

// buildMessage writes a plain-text email with the CSV as an attachment.
func buildMessage(from, replyTo string, m Mail) []byte {
	const boundary = "snout-boundary"
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", "Snout by Badger <"+from+">")
	header("To", m.To)
	if replyTo != "" {
		header("Reply-To", replyTo)
	}
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n", boundary)
	b.WriteString(strings.ReplaceAll(m.Body, "\n", "\r\n"))
	b.WriteString("\r\n")

	name := mime.QEncoding.Encode("utf-8", m.Filename)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/csv; charset=utf-8; name=\"%s\"\r\n", boundary, name)
	fmt.Fprintf(&b, "Content-Disposition: attachment; filename=\"%s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n", name)
	enc := base64.StdEncoding.EncodeToString(m.CSV)
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}
