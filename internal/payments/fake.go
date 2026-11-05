package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"shigoto/internal/core"
)

var _ Provider = (*Fake)(nil)

// FakeSendInvoiceCall records one SendInvoice call made against a Fake.
type FakeSendInvoiceCall struct {
	Invoice     core.Invoice
	Lines       []core.InvoiceLine
	CustomerRef string
}

// Fake is an in-memory payments.Provider for tests. ParseWebhook verifies
// a Stripe-style "t=<unix>,v1=<hex hmac-sha256>" signature over the raw
// payload using Secret, so webhook tests exercise the real verification
// scheme without any network calls.
type Fake struct {
	// Secret is the HMAC secret ParseWebhook verifies signatures against.
	Secret string

	// SendInvoiceErr, if set, is returned once by the next SendInvoice
	// call and then cleared, to simulate a one-off send failure.
	SendInvoiceErr error

	mu          sync.Mutex
	nextID      int
	customers   []core.Contact
	sendInvoice []FakeSendInvoiceCall
	voided      []string
}

// fakeWebhookPayload is the JSON shape Fake.ParseWebhook decodes. It's not
// Stripe's event shape (Fake isn't a Stripe double), only the signing
// scheme matches.
type fakeWebhookPayload struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	InvoiceRef string `json:"invoice_ref"`
	PaymentRef string `json:"payment_ref"`
	Amount     int64  `json:"amount"`
	PaidOn     string `json:"paid_on"`
}

// EnsureCustomer returns a deterministic fake customer ref and records the
// call.
func (f *Fake) EnsureCustomer(_ context.Context, c core.Contact) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	ref := fmt.Sprintf("cus_fake_%d", f.nextID)
	f.customers = append(f.customers, c)
	return ref, nil
}

// SendInvoice returns SendInvoiceErr once if set, otherwise a deterministic
// fake reference and hosted URL, recording the call.
func (f *Fake) SendInvoice(_ context.Context, inv core.Invoice, lines []core.InvoiceLine, customerRef string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.SendInvoiceErr != nil {
		err := f.SendInvoiceErr
		f.SendInvoiceErr = nil
		return "", "", err
	}

	f.nextID++
	ref := fmt.Sprintf("in_fake_%d", f.nextID)
	f.sendInvoice = append(f.sendInvoice, FakeSendInvoiceCall{Invoice: inv, Lines: lines, CustomerRef: customerRef})
	return ref, "https://fake.example/invoices/" + ref, nil
}

// VoidInvoice records ref as voided.
func (f *Fake) VoidInvoice(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.voided = append(f.voided, ref)
	return nil
}

// Calls returns the SendInvoice calls made so far, for test assertions.
func (f *Fake) Calls() []FakeSendInvoiceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeSendInvoiceCall(nil), f.sendInvoice...)
}

// Sign returns a Stripe-style "t=<unix>,v1=<hex>" header for payload, signed
// with f.Secret, for tests to attach as the request's signature header.
func (f *Fake) Sign(payload []byte, at time.Time) string {
	sig := hmacSignature(f.Secret, at, payload)
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(sig))
}

func hmacSignature(secret string, t time.Time, payload []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.", t.Unix())
	mac.Write(payload)
	return mac.Sum(nil)
}

// ParseWebhook verifies payload's "t=…,v1=…" signature against f.Secret and
// decodes it as a fakeWebhookPayload.
func (f *Fake) ParseWebhook(payload []byte, sigHeader string) (Event, error) {
	ts, sig, err := parseFakeSigHeader(sigHeader)
	if err != nil {
		return Event{}, ErrBadSignature
	}
	want := hmacSignature(f.Secret, time.Unix(ts, 0), payload)
	got, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(want, got) {
		return Event{}, ErrBadSignature
	}

	var p fakeWebhookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return Event{}, fmt.Errorf("decoding fake webhook payload: %w", err)
	}
	paidOn, err := time.Parse(time.RFC3339, p.PaidOn)
	if err != nil {
		return Event{}, fmt.Errorf("parsing paid_on: %w", err)
	}

	return Event{
		ID:         p.ID,
		Kind:       p.Kind,
		InvoiceRef: p.InvoiceRef,
		PaymentRef: p.PaymentRef,
		Amount:     core.Cents(p.Amount),
		PaidOn:     paidOn,
	}, nil
}

// parseFakeSigHeader parses "t=<unix>,v1=<hex>" into its timestamp and hex
// signature.
func parseFakeSigHeader(header string) (int64, string, error) {
	var ts int64
	var sig string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			n, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return 0, "", err
			}
			ts = n
		case "v1":
			sig = kv[1]
		}
	}
	if sig == "" {
		return 0, "", fmt.Errorf("no v1 signature in header")
	}
	return ts, sig, nil
}
