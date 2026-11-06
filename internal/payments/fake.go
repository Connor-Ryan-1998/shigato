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
	Today       time.Time
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

	// SendInvoiceErrAfterItems is how many invoice items that failing
	// call creates before returning SendInvoiceErr (0: fail before any
	// item), to simulate a partial send.
	SendInvoiceErrAfterItems int

	// VoidInvoiceErr, if set, is returned once by the next VoidInvoice
	// call and then cleared, to simulate a one-off void failure.
	VoidInvoiceErr error

	// Payouts is keyed by Payout.Ref, settable by tests for GetPayout and
	// ListPayouts.
	Payouts map[string]Payout
	// Transactions is keyed by payout ref, settable by tests for
	// PayoutTransactions.
	Transactions map[string][]BalanceTxn
	// GetPayoutErr, if set, is returned once by the next GetPayout call
	// and then cleared.
	GetPayoutErr error

	mu          sync.Mutex
	nextID      int
	customers   []core.Contact
	sendInvoice []FakeSendInvoiceCall
	voided      []string

	// Provider-side objects keyed by idempotency key, modelling Stripe's
	// replay of a retried write instead of creating a duplicate.
	customerRefs map[string]string          // CustomerKey -> ref
	invoiceRefs  map[string]string          // InvoiceKey -> ref
	items        map[string]map[string]bool // invoice ref -> item keys
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

	ShigotoInvoiceID int64 `json:"shigoto_invoice_id"`
}

// EnsureCustomer records the call and returns c's fake customer ref,
// creating it only the first time c's CustomerKey is seen.
func (f *Fake) EnsureCustomer(_ context.Context, c core.Contact) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.customers = append(f.customers, c)
	if f.customerRefs == nil {
		f.customerRefs = map[string]string{}
	}
	key := CustomerKey(c)
	if ref, ok := f.customerRefs[key]; ok {
		return ref, nil
	}
	f.nextID++
	ref := fmt.Sprintf("cus_fake_%d", f.nextID)
	f.customerRefs[key] = ref
	return ref, nil
}

// SendInvoice models Stripe's idempotent create-invoice-then-items flow:
// the invoice and each item are created only the first time their key is
// seen. If SendInvoiceErr is set, it is returned once (then cleared) after
// SendInvoiceErrAfterItems items; otherwise the call is recorded and the
// invoice's ref and hosted URL are returned.
func (f *Fake) SendInvoice(_ context.Context, inv core.Invoice, lines []core.InvoiceLine, customerRef string, today time.Time) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.invoiceRefs == nil {
		f.invoiceRefs = map[string]string{}
		f.items = map[string]map[string]bool{}
	}
	key := InvoiceKey(inv)
	ref, ok := f.invoiceRefs[key]
	if !ok {
		f.nextID++
		ref = fmt.Sprintf("in_fake_%d", f.nextID)
		f.invoiceRefs[key] = ref
		f.items[ref] = map[string]bool{}
	}

	failErr := f.SendInvoiceErr
	f.SendInvoiceErr = nil
	for i, l := range lines {
		if failErr != nil && i == f.SendInvoiceErrAfterItems {
			return "", "", failErr
		}
		f.items[ref][fmt.Sprintf("%s-item-%d", key, l.ID)] = true
	}
	if failErr != nil {
		return "", "", failErr
	}

	f.sendInvoice = append(f.sendInvoice, FakeSendInvoiceCall{Invoice: inv, Lines: lines, CustomerRef: customerRef, Today: today})
	return ref, "https://fake.example/invoices/" + ref, nil
}

// VoidInvoice returns VoidInvoiceErr once if set, otherwise records ref as
// voided.
func (f *Fake) VoidInvoice(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.VoidInvoiceErr != nil {
		err := f.VoidInvoiceErr
		f.VoidInvoiceErr = nil
		return err
	}

	f.voided = append(f.voided, ref)
	return nil
}

// GetPayout returns the payout registered under f.Payouts[payoutRef], or
// GetPayoutErr if set (once, then cleared).
func (f *Fake) GetPayout(_ context.Context, payoutRef string) (Payout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetPayoutErr != nil {
		err := f.GetPayoutErr
		f.GetPayoutErr = nil
		return Payout{}, err
	}
	p, ok := f.Payouts[payoutRef]
	if !ok {
		return Payout{}, fmt.Errorf("%w: %q", ErrPayoutNotFound, payoutRef)
	}
	return p, nil
}

// ListPayouts returns every registered payout whose ArrivalOn falls within
// [arrivalFrom, arrivalTo], inclusive.
func (f *Fake) ListPayouts(_ context.Context, arrivalFrom, arrivalTo time.Time) ([]Payout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Payout
	for _, p := range f.Payouts {
		if !p.ArrivalOn.Before(arrivalFrom) && !p.ArrivalOn.After(arrivalTo) {
			out = append(out, p)
		}
	}
	return out, nil
}

// PayoutTransactions returns the balance transactions registered under
// f.Transactions[payoutRef].
func (f *Fake) PayoutTransactions(_ context.Context, payoutRef string) ([]BalanceTxn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]BalanceTxn(nil), f.Transactions[payoutRef]...), nil
}

// Calls returns the successful SendInvoice calls made so far, for test
// assertions (e.g. that a retried send didn't double-send).
func (f *Fake) Calls() []FakeSendInvoiceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeSendInvoiceCall(nil), f.sendInvoice...)
}

// VoidedRefs returns the provider_refs VoidInvoice has successfully voided
// so far, for test assertions.
func (f *Fake) VoidedRefs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.voided...)
}

// ItemCount returns how many distinct invoice items exist on the fake
// provider invoice ref, for asserting a retried send didn't duplicate any.
func (f *Fake) ItemCount(ref string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items[ref])
}

// CustomerRefs returns the distinct fake provider customers created so far.
func (f *Fake) CustomerRefs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	refs := make([]string, 0, len(f.customerRefs))
	for _, r := range f.customerRefs {
		refs = append(refs, r)
	}
	return refs
}

// Customers returns the contacts EnsureCustomer has been called with so
// far, for test assertions.
func (f *Fake) Customers() []core.Contact {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.Contact(nil), f.customers...)
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
		ID:               p.ID,
		Kind:             p.Kind,
		InvoiceRef:       p.InvoiceRef,
		PaymentRef:       p.PaymentRef,
		Amount:           core.Cents(p.Amount),
		PaidOn:           paidOn,
		ShigotoInvoiceID: p.ShigotoInvoiceID,
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
