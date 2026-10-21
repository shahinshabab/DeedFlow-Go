// Package mail sends the application's few emails (the receipts alert, the
// test message): through the Amazon SES API when AWS_SES_SENDER is set,
// otherwise through SMTP with the EMAIL_* settings.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/config"
)

// Message is a plain-text email with an optional HTML alternative.
type Message struct {
	From    string
	To      []string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, m *Message) error
	// Description says how mail goes out ("via SES", "via smtp host:port").
	Description() string
}

// New picks the sender the configuration asks for.
func New(cfg *config.Config) Sender {
	if cfg.AWSSESSender != "" {
		return &SES{Region: cfg.AWSRegion, Default: cfg.DefaultFromEmail}
	}
	return &SMTP{Host: cfg.EmailHost, Port: cfg.EmailPort, User: cfg.EmailHostUser, Password: cfg.EmailHostPass,
		StartTLS: cfg.EmailUseTLS, Default: cfg.DefaultFromEmail, Timeout: 30 * time.Second}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func qp(s string) string {
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return buf.String()
}

// Bytes renders the message as RFC 5322 text (CRLF line endings).
func (m *Message) Bytes() []byte {
	var b bytes.Buffer
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\r\n", args...) }
	line("MIME-Version: 1.0")
	line("Subject: %s", mime.QEncoding.Encode("utf-8", m.Subject))
	line("From: %s", m.From)
	line("To: %s", strings.Join(m.To, ", "))
	line("Date: %s", time.Now().Format(time.RFC1123Z))
	line("Message-ID: <%s@deedflow>", randomHex(12))
	part := func(contentType, body string) {
		line("Content-Type: %s; charset=utf-8", contentType)
		line("Content-Transfer-Encoding: quoted-printable")
		line("")
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(qp(body), "\r\n", "\n"), "\n", "\r\n"))
		line("")
	}
	if m.HTML == "" {
		part("text/plain", m.Text)
		return b.Bytes()
	}
	boundary := "df" + randomHex(12)
	line("Content-Type: multipart/alternative; boundary=\"%s\"", boundary)
	line("")
	line("--%s", boundary)
	part("text/plain", m.Text)
	line("--%s", boundary)
	part("text/html", m.HTML)
	line("--%s--", boundary)
	return b.Bytes()
}

func (m *Message) fill(def string) error {
	if m.From == "" {
		m.From = def
	}
	if len(m.To) == 0 {
		return errors.New("the message has no recipients")
	}
	return nil
}

func addressOnly(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}
	return s
}

// SMTP sends through an SMTP server, upgrading with STARTTLS when asked to.
type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	StartTLS bool
	Default  string
	Timeout  time.Duration
}

// Description implements Sender.
func (s *SMTP) Description() string { return fmt.Sprintf("SMTP %s:%d", s.Host, s.Port) }

// Send implements Sender.
func (s *SMTP) Send(ctx context.Context, m *Message) error {
	if err := m.fill(s.Default); err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if s.StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the mail server doesn't offer STARTTLS")
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return err
		}
	}
	if s.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(addressOnly(m.From)); err != nil {
		return err
	}
	for _, to := range m.To {
		if err := c.Rcpt(addressOnly(to)); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(m.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// SES sends through the Amazon SES API (SendRawEmail), signed with the
// AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY from the environment.
type SES struct {
	Region  string
	Default string
	// Endpoint overrides https://email.<region>.amazonaws.com (tests).
	Endpoint string
	// Client is the HTTP client to use (nil = a default with a timeout).
	Client httpDoer
	// Now is the clock (nil = time.Now).
	Now func() time.Time
}

// Description implements Sender.
func (s *SES) Description() string { return "Amazon SES (" + s.Region + ")" }

// Send implements Sender.
func (s *SES) Send(ctx context.Context, m *Message) error {
	if err := m.fill(s.Default); err != nil {
		return err
	}
	access, secret, session := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_SESSION_TOKEN")
	if access == "" || secret == "" {
		return errors.New("AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY are not set")
	}
	form := formValues{{"Action", "SendRawEmail"}, {"Version", "2010-12-01"}, {"Source", m.From}}
	for i, to := range m.To {
		form = append(form, [2]string{fmt.Sprintf("Destinations.member.%d", i+1), addressOnly(to)})
	}
	form = append(form, [2]string{"RawMessage.Data", base64.StdEncoding.EncodeToString(m.Bytes())})
	return s.post(ctx, form.encode(), access, secret, session)
}
