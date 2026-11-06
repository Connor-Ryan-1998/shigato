package mail

import (
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
