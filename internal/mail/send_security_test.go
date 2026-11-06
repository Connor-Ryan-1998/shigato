package mail

import (
	"context"
	"strings"
	"testing"

	"shigoto/internal/config"
)

// TestSendRefusesWhenServerLacksSTARTTLS covers the first security refusal:
// in "starttls" mode, a server whose EHLO response doesn't advertise
// STARTTLS must abort the send before any credentials or mail commands are
// sent.
func TestSendRefusesWhenServerLacksSTARTTLS(t *testing.T) {
	srv := startFakeSMTPServer(t, func(line string) (string, bool) {
		switch {
		case strings.HasPrefix(line, "EHLO"):
			// No STARTTLS in the extension list.
			return "250 fake.local\r\n", false
		case strings.HasPrefix(line, "QUIT"):
			return "221 Bye\r\n", true
		default:
			return "250 OK\r\n", false
		}
	})
	host, port := srv.hostPort(t)

	m := New(config.SMTP{
		Host:     host,
		Port:     port,
		TLS:      "starttls",
		From:     "from@example.com",
		Username: "user",
		Password: "pass",
	})
	err := m.Send(context.Background(), Message{To: "to@example.com", Subject: "hi", Text: "t", HTML: "<p>t</p>"})
	if err == nil {
		t.Fatal("Send: expected an error when the server doesn't advertise STARTTLS, got nil")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("Send error = %q, want it to mention STARTTLS", err)
	}

	if auth := srv.linesWithPrefix("AUTH"); len(auth) != 0 {
		t.Errorf("server received AUTH command(s) %v, want none sent before a failed STARTTLS check", auth)
	}
	if mail := srv.linesWithPrefix("MAIL FROM"); len(mail) != 0 {
		t.Errorf("server received MAIL FROM command(s) %v, want none sent before a failed STARTTLS check", mail)
	}
}

// TestSendRefusesAuthWhenSTARTTLSUpgradeFails covers the second security
// refusal: the server advertises STARTTLS, but the TLS handshake itself
// fails (here, by the server dropping the connection right after agreeing
// to start TLS). AUTH must never be sent in that case.
func TestSendRefusesAuthWhenSTARTTLSUpgradeFails(t *testing.T) {
	srv := startFakeSMTPServer(t, func(line string) (string, bool) {
		switch {
		case strings.HasPrefix(line, "EHLO"):
			return "250-fake.local\r\n250 STARTTLS\r\n", false
		case strings.HasPrefix(line, "STARTTLS"):
			// Agree to start TLS, then immediately close instead of
			// performing the handshake, so the client's TLS upgrade fails.
			return "220 Ready to start TLS\r\n", true
		case strings.HasPrefix(line, "QUIT"):
			return "221 Bye\r\n", true
		default:
			return "250 OK\r\n", false
		}
	})
	host, port := srv.hostPort(t)

	m := New(config.SMTP{
		Host:     host,
		Port:     port,
		TLS:      "starttls",
		From:     "from@example.com",
		Username: "user",
		Password: "pass",
	})
	err := m.Send(context.Background(), Message{To: "to@example.com", Subject: "hi", Text: "t", HTML: "<p>t</p>"})
	if err == nil {
		t.Fatal("Send: expected an error when the STARTTLS upgrade fails, got nil")
	}
	if !strings.Contains(err.Error(), "starttls") {
		t.Errorf("Send error = %q, want it to mention starttls", err)
	}

	if auth := srv.linesWithPrefix("AUTH"); len(auth) != 0 {
		t.Errorf("server received AUTH command(s) %v, want none sent before a successful TLS upgrade", auth)
	}
}
