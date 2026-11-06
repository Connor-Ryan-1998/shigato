// Package mail sends outgoing email over SMTP.
package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"shigoto/internal/config"
)

// sendTimeout bounds the whole dial-to-quit SMTP conversation.
const sendTimeout = 15 * time.Second

// Message is an outgoing email. To and Subject are sanitised (CR/LF
// stripped) before being placed in headers.
type Message struct {
	To, Subject, Text, HTML string
}

// Mailer sends Messages via Send. New returns nil when SMTP isn't
// configured, so callers must check for a nil *Mailer before calling Send.
type Mailer struct {
	From string
	Send func(ctx context.Context, m Message) error
}

// New builds a Mailer from cfg. It returns nil when cfg.Host is empty,
// meaning email is disabled.
func New(cfg config.SMTP) *Mailer {
	if cfg.Host == "" {
		return nil
	}
	m := &Mailer{From: cfg.From}
	m.Send = func(ctx context.Context, msg Message) error {
		return send(ctx, cfg, m.From, msg)
	}
	return m
}

// sanitizeHeader strips CR and LF so a caller-supplied value can't inject
// extra headers (e.g. "...\r\nBcc:attacker@evil.example").
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// crlf normalises line endings to CRLF, as required for SMTP message bodies.
func crlf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// writeQuotedPrintable writes s to w quoted-printable encoded, matching the
// part's Content-Transfer-Encoding: quoted-printable header.
func writeQuotedPrintable(w io.Writer, s string) error {
	qw := quotedprintable.NewWriter(w)
	if _, err := qw.Write([]byte(crlf(s))); err != nil {
		return err
	}
	return qw.Close()
}

// buildMessage renders msg as a multipart/alternative message with CRLF
// line endings throughout.
func buildMessage(from string, msg Message) ([]byte, error) {
	to := sanitizeHeader(msg.To)
	// mime.QEncoding.Encode returns its input unchanged when it's already a
	// valid ASCII string, so a plain-ASCII subject is left untouched.
	subject := mime.QEncoding.Encode("utf-8", sanitizeHeader(msg.Subject))

	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	boundary := mw.Boundary()

	textHeader := textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	}
	textPart, err := mw.CreatePart(textHeader)
	if err != nil {
		return nil, fmt.Errorf("mail: text part: %w", err)
	}
	if err := writeQuotedPrintable(textPart, msg.Text); err != nil {
		return nil, fmt.Errorf("mail: writing text part: %w", err)
	}

	htmlHeader := textproto.MIMEHeader{
		"Content-Type":              {"text/html; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	}
	htmlPart, err := mw.CreatePart(htmlHeader)
	if err != nil {
		return nil, fmt.Errorf("mail: html part: %w", err)
	}
	if err := writeQuotedPrintable(htmlPart, msg.HTML); err != nil {
		return nil, fmt.Errorf("mail: writing html part: %w", err)
	}

	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("mail: closing multipart writer: %w", err)
	}

	var raw bytes.Buffer
	raw.WriteString("From: " + from + "\r\n")
	raw.WriteString("To: " + to + "\r\n")
	raw.WriteString("Subject: " + subject + "\r\n")
	raw.WriteString("MIME-Version: 1.0\r\n")
	raw.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	raw.WriteString("\r\n")
	raw.Write(parts.Bytes())
	return raw.Bytes(), nil
}

// send delivers msg over SMTP within a 15-second deadline, honouring cfg's
// TLS mode. It refuses to send AUTH credentials over a connection that
// hasn't upgraded to TLS, unless TLS mode is "none" (only ever valid for a
// loopback/mailpit host per config validation).
func send(ctx context.Context, cfg config.SMTP, from string, msg Message) error {
	raw, err := buildMessage(from, msg)
	if err != nil {
		return err
	}
	to := sanitizeHeader(msg.To)

	deadline := time.Now().Add(sendTimeout)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	dialer := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	if cfg.TLS == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: cfg.Host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("mail: set deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("mail: smtp handshake: %w", err)
	}
	defer client.Close()

	// "tls" (implicit TLS) and "none" (loopback-only, validated at config
	// load) are both treated as already secure for AUTH purposes.
	secure := cfg.TLS != "starttls"
	if cfg.TLS == "starttls" {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("mail: server at %s does not support STARTTLS", addr)
		}
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
		secure = true
	}

	if cfg.Username != "" {
		if !secure {
			return fmt.Errorf("mail: refusing to send AUTH credentials over a non-TLS connection")
		}
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("mail: writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: closing message: %w", err)
	}
	return client.Quit()
}
