//go:build integration

package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"shigoto/internal/config"
)

// mailpitMessages is the shape of GET /api/v1/messages that we care about.
type mailpitMessages struct {
	Messages []struct {
		To []struct {
			Address string `json:"Address"`
		} `json:"To"`
		Subject string `json:"Subject"`
	} `json:"messages"`
}

func TestSendDeliversToMailpit(t *testing.T) {
	cfg := config.SMTP{
		Host: "localhost",
		Port: 1025,
		From: "noreply@shigoto.local",
		TLS:  "none",
	}
	m := New(cfg)
	if m == nil {
		t.Fatal("New(cfg) = nil, want a Mailer")
	}

	subject := fmt.Sprintf("shigoto integration test %d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Send(ctx, Message{
		To:      "integration-test@example.com",
		Subject: subject,
		Text:    "plain body",
		HTML:    "<p>html body</p>",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://localhost:8025/api/v1/messages")
		if err != nil {
			t.Fatalf("querying mailpit: %v", err)
		}
		var msgs mailpitMessages
		err = json.NewDecoder(resp.Body).Decode(&msgs)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("decoding mailpit response: %v", err)
		}
		for _, msg := range msgs.Messages {
			if msg.Subject != subject {
				continue
			}
			for _, to := range msg.To {
				if strings.EqualFold(to.Address, "integration-test@example.com") {
					found = true
				}
			}
		}
		if found {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !found {
		t.Fatalf("message with subject %q not found in mailpit", subject)
	}
}
