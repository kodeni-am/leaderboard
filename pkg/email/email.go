// Package email sends transactional mail (verification, password reset). It
// abstracts the transport so local/dev runs need no mail server (ConsoleSender
// logs the link) while production uses SMTP.
package email

import (
	"context"
	"fmt"
	"io"
	"net/smtp"
	"os"
	"strings"
	"sync"
)

// Message is a single email.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// ConsoleSender writes messages to a writer (stdout by default) instead of
// sending them — so signup/verify/reset flows work locally with no mail server.
type ConsoleSender struct {
	mu  sync.Mutex
	out io.Writer
}

func NewConsoleSender(out io.Writer) *ConsoleSender {
	if out == nil {
		out = os.Stdout
	}
	return &ConsoleSender{out: out}
}

func (s *ConsoleSender) Send(_ context.Context, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.out, "\n──────── EMAIL (console) ────────\nTo:      %s\nSubject: %s\n\n%s\n─────────────────────────────────\n\n", m.To, m.Subject, m.Text)
	return nil
}

// SMTPSender sends via an SMTP server.
type SMTPSender struct {
	addr string
	from string
	auth smtp.Auth
}

// NewSMTPSender builds an SMTP sender. If username is empty, no auth is used
// (e.g. a local relay / MailHog).
func NewSMTPSender(host string, port int, username, password, from string) *SMTPSender {
	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	return &SMTPSender{addr: fmt.Sprintf("%s:%d", host, port), from: from, auth: auth}
}

func (s *SMTPSender) Send(_ context.Context, m Message) error {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", s.from)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", m.Subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(m.Text)
	return smtp.SendMail(s.addr, s.auth, s.from, []string{m.To}, []byte(b.String()))
}
