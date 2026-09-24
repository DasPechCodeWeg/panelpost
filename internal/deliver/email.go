// Package deliver sends finished reports by email, to webhooks and to a
// folder.
package deliver

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	mail "github.com/wneessen/go-mail"

	"github.com/DasPechCodeWeg/panelpost/internal/model"
)

// Attachment is a file attached to an email.
type Attachment struct {
	Name        string
	Data        []byte
	ContentType string
}

// Email is one message to send.
type Email struct {
	To, Cc, Bcc []string
	Subject     string
	Text        string
	FromName    string // overrides the configured sender name (white-label)
	Attachments []Attachment
}

// Mailer sends email through SMTP.
type Mailer struct {
	SMTP model.SMTP
	// Timeout bounds connecting and sending. Defaults to 60 seconds.
	Timeout time.Duration
}

func (m *Mailer) client() (*mail.Client, error) {
	s := m.SMTP
	if !s.Configured() {
		return nil, errors.New("email is not set up yet: add an SMTP server under Settings")
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	port := s.Port
	opts := []mail.Option{mail.WithTimeout(timeout)}
	switch strings.ToLower(s.TLS) {
	case "tls", "ssl", "implicit":
		if port == 0 {
			port = 465
		}
		opts = append(opts, mail.WithSSL())
	case "none":
		if port == 0 {
			port = 25
		}
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	default: // starttls
		if port == 0 {
			port = 587
		}
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	}
	opts = append(opts, mail.WithPort(port))
	if s.Insecure {
		opts = append(opts, mail.WithTLSConfig(&tls.Config{InsecureSkipVerify: true, ServerName: s.Host})) //nolint:gosec // explicit opt-in for internal relays
	}
	if s.Username != "" {
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthAutoDiscover), mail.WithUsername(s.Username), mail.WithPassword(s.Password))
	}
	return mail.NewClient(s.Host, opts...)
}

// Send delivers one message.
func (m *Mailer) Send(ctx context.Context, e Email) error {
	if !m.SMTP.Configured() {
		return errors.New("email is not set up yet: add an SMTP server under Settings")
	}
	if len(e.To)+len(e.Cc)+len(e.Bcc) == 0 {
		return errors.New("no recipients")
	}
	msg := mail.NewMsg()
	name := m.SMTP.FromName
	if e.FromName != "" {
		name = e.FromName
	}
	var err error
	if name != "" {
		err = msg.FromFormat(name, m.SMTP.From)
	} else {
		err = msg.From(m.SMTP.From)
	}
	if err != nil {
		return fmt.Errorf("sender address %q: %w", m.SMTP.From, err)
	}
	if len(e.To) > 0 {
		if err := msg.To(e.To...); err != nil {
			return fmt.Errorf("recipient: %w", err)
		}
	}
	if len(e.Cc) > 0 {
		if err := msg.Cc(e.Cc...); err != nil {
			return fmt.Errorf("cc: %w", err)
		}
	}
	if len(e.Bcc) > 0 {
		if err := msg.Bcc(e.Bcc...); err != nil {
			return fmt.Errorf("bcc: %w", err)
		}
	}
	msg.Subject(e.Subject)
	msg.SetMessageID()
	msg.SetDate()
	msg.SetBodyString(mail.TypeTextPlain, e.Text)
	msg.AddAlternativeString(mail.TypeTextHTML, textToHTML(e.Text))
	for _, a := range e.Attachments {
		ct := a.ContentType
		if ct == "" {
			ct = "application/pdf"
		}
		if err := msg.AttachReader(a.Name, bytes.NewReader(a.Data), mail.WithFileContentType(mail.ContentType(ct))); err != nil {
			return fmt.Errorf("attach %s: %w", a.Name, err)
		}
	}
	c, err := m.client()
	if err != nil {
		return err
	}
	if err := c.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("send email via %s: %w", m.SMTP.Host, err)
	}
	return nil
}

// textToHTML renders the plain text body as simple, safe HTML.
func textToHTML(text string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.55;color:#1c2230">`)
	for _, para := range strings.Split(strings.TrimSpace(text), "\n\n") {
		b.WriteString("<p>")
		b.WriteString(strings.ReplaceAll(html.EscapeString(para), "\n", "<br>"))
		b.WriteString("</p>")
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

// Placeholders are replaced in subjects and bodies.
type Placeholders struct {
	Report    string
	Dashboard string
	Period    string
	Target    string // burst label, e.g. the client name
	Company   string
	Date      string
	Pages     int
}

// Expand replaces {{report}}, {{dashboard}}, {{period}}, {{target}},
// {{company}}, {{date}} and {{pages}}.
func (p Placeholders) Expand(s string) string {
	r := strings.NewReplacer(
		"{{report}}", p.Report,
		"{{dashboard}}", p.Dashboard,
		"{{period}}", p.Period,
		"{{target}}", p.Target,
		"{{company}}", p.Company,
		"{{date}}", p.Date,
		"{{pages}}", fmt.Sprint(p.Pages),
	)
	return r.Replace(s)
}

// DefaultSubject is used when a report has no subject.
const DefaultSubject = "{{report}} · {{period}}"

// DefaultBody is used when a report has no body.
const DefaultBody = `Hello,

Attached is the {{report}} report for {{period}}.

It was generated automatically from the "{{dashboard}}" dashboard.`
