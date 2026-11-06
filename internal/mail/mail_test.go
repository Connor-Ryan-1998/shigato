package mail

import (
	"mime"
	"strings"
	"testing"

	"shigoto/internal/config"
)

func TestNewReturnsNilWhenNotConfigured(t *testing.T) {
	if m := New(config.SMTP{}); m != nil {
		t.Fatalf("New(empty) = %v, want nil", m)
	}
}

func TestNewReturnsMailerWhenConfigured(t *testing.T) {
	m := New(config.SMTP{Host: "mailpit", Port: 1025, From: "noreply@example.com", TLS: "none"})
	if m == nil {
		t.Fatal("New(configured) = nil, want a Mailer")
	}
	if m.From != "noreply@example.com" {
		t.Errorf("From = %q, want %q", m.From, "noreply@example.com")
	}
	if m.Send == nil {
		t.Error("Send is nil, want a function")
	}
}

func TestBuildMessageContainsBothParts(t *testing.T) {
	raw, err := buildMessage("noreply@example.com", Message{
		To:      "someone@example.com",
		Subject: "Hello",
		Text:    "plain body",
		HTML:    "<p>html body</p>",
	})
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, "Content-Type: text/plain; charset=utf-8") {
		t.Errorf("message missing text/plain part:\n%s", s)
	}
	if !strings.Contains(s, "Content-Type: text/html; charset=utf-8") {
		t.Errorf("message missing text/html part:\n%s", s)
	}
	if !strings.Contains(s, "plain body") {
		t.Errorf("message missing text body:\n%s", s)
	}
	if !strings.Contains(s, "<p>html body</p>") {
		t.Errorf("message missing html body:\n%s", s)
	}
	if !strings.Contains(s, "Content-Type: multipart/alternative;") {
		t.Errorf("message missing multipart/alternative content type:\n%s", s)
	}
	if got := strings.Count(s, "Content-Transfer-Encoding: quoted-printable"); got != 2 {
		t.Errorf("Content-Transfer-Encoding: quoted-printable appears %d times, want 2 (one per part):\n%s", got, s)
	}
}

func TestBuildMessageEncodesPartsQuotedPrintable(t *testing.T) {
	raw, err := buildMessage("noreply@example.com", Message{
		To:      "someone@example.com",
		Subject: "Hello",
		Text:    "100% = done",
		HTML:    "<p>100% = done</p>",
	})
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	s := string(raw)
	// '=' is quoted-printable's escape character, so a literal '=' in the
	// body must itself be encoded as "=3D", never passed through raw.
	if !strings.Contains(s, "=3D") {
		t.Errorf("want literal '=' encoded as quoted-printable \"=3D\", got:\n%s", s)
	}
}

func TestBuildMessageEncodesNonASCIISubject(t *testing.T) {
	subject := "Héllo wörld"
	raw, err := buildMessage("noreply@example.com", Message{
		To:      "someone@example.com",
		Subject: subject,
		Text:    "body",
	})
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "Subject: "+subject) {
		t.Errorf("want non-ASCII subject RFC 2047 encoded, found it raw in:\n%s", s)
	}
	headerVal := headerValue(t, raw, "Subject")
	dec := new(mime.WordDecoder)
	got, err := dec.DecodeHeader(headerVal)
	if err != nil {
		t.Fatalf("decoding Subject header %q: %v", headerVal, err)
	}
	if got != subject {
		t.Errorf("decoded Subject = %q, want %q", got, subject)
	}
}

// headerValue extracts the value of the first "name: value" header line
// from a raw message built by buildMessage.
func headerValue(t *testing.T, raw []byte, name string) string {
	t.Helper()
	prefix := name + ": "
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("no %q header found in:\n%s", name, raw)
	return ""
}

func TestBuildMessageUsesCRLF(t *testing.T) {
	raw, err := buildMessage("noreply@example.com", Message{
		To:      "someone@example.com",
		Subject: "Hello",
		Text:    "line one\nline two",
	})
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	for i, b := range raw {
		if b == '\n' && (i == 0 || raw[i-1] != '\r') {
			t.Fatalf("message contains a bare LF at byte %d, want CRLF throughout:\n%q", i, raw)
		}
	}
}

func TestBuildMessageSanitisesHeaderInjection(t *testing.T) {
	raw, err := buildMessage("noreply@example.com", Message{
		To:      "someone@example.com\r\nBcc:attacker@evil.example",
		Subject: "Hello\r\nX-Injected: yes",
		Text:    "body",
	})
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "\r\nBcc:") {
		t.Errorf("message contains an injected Bcc header line:\n%s", s)
	}
	if strings.Contains(s, "\r\nX-Injected:") {
		t.Errorf("message contains an injected X-Injected header line:\n%s", s)
	}
	if !strings.Contains(s, "To: someone@example.comBcc:attacker@evil.example\r\n") {
		t.Errorf("want sanitised To header with CR/LF stripped, got:\n%s", s)
	}
	if !strings.Contains(s, "Subject: HelloX-Injected: yes\r\n") {
		t.Errorf("want sanitised Subject header with CR/LF stripped, got:\n%s", s)
	}
}
