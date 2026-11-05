# shigoto Plan 4: Reconciliation, Expenses and Tax Reports

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** At tax time:
- Import bank CSVs.
- Match each line to an invoice payment, a Stripe payout (fees become an expense), or an expense (split lines, receipt attached), or ignore it.
- Produce a cash-basis P&L, by org, property or project, and an Australian BAS. Every figure traces back to rows and can be exported as CSV.

**Architecture:**
- `internal/importers` holds the pluggable bank-line `Source` (CSV first). The spec allows exactly two interfaces, and this is the second; the other is `payments.Provider`.
- `internal/recon` is the `reconciliation` module: bank accounts, imports, bank lines, categories, expenses, receipts, matching and Stripe payouts.
- `internal/reports` is always on. It reads only invoices, invoice lines, payments and expense lines.

**Tech Stack:** Go 1.27 stdlib (`encoding/csv`, `mime/multipart`, `crypto/sha256`, `net/http.DetectContentType`), pgx/v5, stripe-go v87, Postgres 18.6. No new dependencies.

**Spec:** `docs/design.md`, sections "Tax-time reconciliation", Data model "Reconciliation", Key flows 3, Constraints "AU tax rules", "Stripe fees and payouts". Follow-ups: `docs/superpowers/plans/2026-11-05-01-followups.md`, section "Plan 4".

## Global Constraints

- Everything in Plans 1–3 still applies:
  - Integer cents.
  - `org_id` on every query.
  - Schema changes only in new migration files. The next one is `0008_`.
  - Calendar dates stored as UTC midnight in `Config.Location`.
  - Errors go through `module.WriteCoreError`.
  - Strict full-replace PUTs.
  - Audit every write.
  - Invoice JSON uses `billing.InvoiceResponse`.
  - No new dependencies.
- **Signs:**
  - Bank line amounts are signed: money in is positive, money out is negative.
  - Expense line amounts are positive and include GST.
- **GST codes and allowed values** are unchanged: `GST`, `FRE`, `INP`, `N-T`.
  - The GST inside an expense line is `core.GSTInInclusive(amount, code)`.
  - An expense line tagged to a residential property is stored as `INP` with GST 0, and the response carries a warning (spec: no GST credits on residential).
  - In an org that is not GST-registered, every expense line has GST 0.
- **Cash basis:**
  - Income counts on `payments.paid_on`. Expenses count on `expenses.spent_on`.
  - Bond invoices (`source = 'bond'`) never appear in P&L or BAS.
  - Draft and void invoices never contribute.
- **Allocating a payment across invoice lines:**
  - Each line's incl-GST share is `DivRound(payment × line_total, invoice_total)`. The last line takes the remainder, so the shares always sum to the payment exactly.
  - The GST part of a share is `DivRound(share × line.gst_amount, line_total)`. The ex-GST part is the share minus its GST part.
- **Income category comes from the invoice source:**
  - `rent` → "Rent"
  - `timesheet` → "Consulting"
  - `manual` → "Other income"
- **BAS figures:**
  - Shown in cents, plus a whole-dollar column truncated toward zero, as the ATO form takes whole dollars.
  - Requires `orgs.gst_registered`, otherwise 422.
- **Financial year:**
  - It starts in `orgs.fy_start_month`, default 7.
  - Quarter `Q1` covers months 7–9 of the FY.
  - A period string like `FY2027-Q1` means Jul–Sep 2026.
- **Out of scope (ruling for this plan):**
  - Credit notes and refunds are deferred.
  - Instead, `billing.Void` refuses an invoice that has any payment (409, "invoice has payments; record a credit note instead"). That closes the double-billing risk in the follow-ups.
  - Gap-filling voided middle rent periods and warnings for edits inside billed periods stay deferred with credit notes.

## Review Focus

1. **The same CSV imported twice, or overlapping exports.** No duplicate bank lines are created. Two genuinely identical transactions on the same day in one file are both kept. Test in Task 2.
2. **One bank line matched twice at the same time** (two tabs, or auto-match plus manual). Exactly one match succeeds, and no duplicate payment or expense exists. Test in Task 4.
3. **A partial payment across a multi-line invoice with mixed GST codes.** The allocated shares sum exactly to the payment, and the BAS 1A plus G1 figures tie back to the detail rows. Test in Task 6.
4. **A Stripe payout whose amount doesn't match the bank line, or that contains a charge shigoto doesn't know.** Returns 409, writes nothing, and leaves the line unmatched. Test in Task 5.
5. **Unmatching a line that created a payment.** The payment is deleted and the invoice goes back from `paid` to `open`. Unmatching an expense link leaves the expense but frees it. Test in Task 4.

---

### Task 1: Money hygiene and void guard (follow-ups)

**Files:**
- Modify:
  - `internal/core/money.go` and `money_test.go`
  - `internal/core/invoices.go` and `invoices_integration_test.go`
  - `internal/billing/billing.go` and `billing_integration_test.go`
  - `internal/payments/stripe/stripe.go` and `stripe_test.go`
  - `docs/superpowers/plans/2026-11-05-01-followups.md`

**Interfaces:**
- Produces:
  - **Input bounds** in `core.CreateDraftInvoice`:
    - `quantity_milli` from 1 to 1,000,000,000.
    - `unit_amount` from 0 to 100,000,000,000 cents.
    - Otherwise `ErrInvalidInput`. This makes `LineAmount` overflow impossible.
  - **GST enforcement:** in an org that is not GST-registered, `CreateDraftInvoice` rejects any line coded `GST` with `ErrInvalidInput` "org is not GST-registered".
    - Rent runs and timesheet runs already pick `FRE` or `INP` correctly.
    - This check guards manual invoices.
  - **Webhook date fallback:** `stripe.ParseWebhook` falls back to the event's `created` time, in `loc`, when `status_transitions.paid_at` is 0.
  - **Void guard:** `billing.Void` returns `core.ErrInvalidState`, with the message "invoice has payments; record a credit note instead", when `SELECT count(*) FROM payments WHERE org_id AND invoice_id` is greater than 0. It is checked under the invoice row lock that `core.VoidInvoice` takes.

- [ ] **Step 1: Write failing tests.**
  - `money_test.go` negative cases:
    - `LineAmount(1500, -12000) == -18000`
    - `GSTOnExclusive(-5, GST) == -1`
    - `GSTInInclusive(-1000, GST) == -91`
    - `Totals` with one negative line sums correctly.
  - Integration:
    - `TestCreateDraftRejectsHugeQuantity` (quantity 1,000,000,001 gives 422).
    - `TestCreateDraftRejectsGSTWhenUnregistered`.
    - `TestVoidRefusesInvoiceWithPayments`: a partial manual payment gives 409, and the invoice stays open.
  - `TestParseWebhookPaidAtZeroUsesEventCreated`.
- [ ] **Step 2: Run them and confirm FAIL.**
- [ ] **Step 3: Implement. Remove the done items from the follow-ups doc's "Plan 4" section, and move credit notes to a new "Later" section.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `fix: bound invoice inputs, enforce gst registration, refuse voiding paid invoices`

### Task 2: Bank accounts and CSV import

**Files:**
- Create:
  - `internal/importers/source.go`
  - `internal/importers/csv.go` and `csv_test.go`
  - `internal/db/migrations/0008_recon.sql`
  - `internal/recon/model.go`, `bank.go` and `routes.go`
  - `internal/recon/bank_integration_test.go` (external `recon_test` package)
- Modify:
  - `internal/httpapi/server.go`: add `recon.Module()` to `modules`. The module name is `"reconciliation"`, which is already in `config.Known`.

**`0008_recon.sql`:**
- `bank_accounts`:
  - `id`, `org_id NOT NULL REFERENCES orgs`, `name text NOT NULL`.
  - `csv_mapping jsonb NOT NULL`, `created_at`, `UNIQUE(org_id, name)`.
- `bank_imports`:
  - `id`, `org_id NOT NULL`, `bank_account_id NOT NULL REFERENCES bank_accounts`.
  - `file_sha256 bytea NOT NULL`, `rows_total int`, `rows_new int`, `imported_at`.
- `bank_lines`:
  - `id`, `org_id NOT NULL`, `bank_account_id NOT NULL REFERENCES bank_accounts`, `import_id NOT NULL REFERENCES bank_imports`.
  - `posted_on date NOT NULL`, `amount bigint NOT NULL CHECK (amount <> 0)`, `description text NOT NULL`.
  - `dedupe_key bytea NOT NULL`.
  - `status text NOT NULL DEFAULT 'unmatched' CHECK (status IN ('unmatched','matched','ignored'))`, `ignore_reason text NULL`.
  - `UNIQUE(bank_account_id, dedupe_key)`.
  - Index on `(org_id, status, posted_on)`.
- `ALTER TABLE payments ADD CONSTRAINT payments_bank_line_fk FOREIGN KEY (bank_line_id) REFERENCES bank_lines(id)`.

**Interfaces:**
- Produces:

```go
// internal/importers
type Line struct{ PostedOn time.Time; Amount core.Cents; Description string }
type Source interface{ Lines(r io.Reader) ([]Line, error) }
type CSVMapping struct {
	HasHeader         bool   `json:"has_header"`
	DateColumn        int    `json:"date_column"`        // 0-based
	DateFormat        string `json:"date_format"`        // one of "02/01/2006", "2006-01-02", "2 Jan 2006"
	AmountColumn      *int   `json:"amount_column"`      // signed amount, or:
	DebitColumn       *int   `json:"debit_column"`       // positive numbers = money out
	CreditColumn      *int   `json:"credit_column"`      // positive numbers = money in
	DescriptionColumn int    `json:"description_column"`
}
func (m CSVMapping) Validate() error          // exactly one of AmountColumn or (Debit+Credit); known DateFormat
func NewCSV(m CSVMapping) Source
func Preset(name string) (CSVMapping, bool)   // "commbank": no header, date 0 "02/01/2006", amount 1, description 2
func DedupeKeys(lines []Line) [][]byte          // sha256(date|amount|description|n) where n = occurrence index of that exact triple within this file
```

- **Amount parsing** accepts these and returns `ErrInvalidInput` with the row number otherwise:
  - `1234.56`, `1,234.56`, `-12.30`, `(12.30)` (negative), `+5.00`, `$12.30`.
  - At most 2 decimal places.
  - A blank debit or credit cell counts as 0.
  - A row whose total is 0 is skipped.
  - Description whitespace is trimmed.
- **Bank account and import functions:**
  - `recon.CreateBankAccount(ctx, q, orgID, name string, m importers.CSVMapping) (BankAccount, error)`.
  - `recon.ListBankAccounts`.
  - `recon.Import(ctx, pool, orgID, accountID int64, file io.Reader) (ImportResult{RowsTotal, RowsNew int}, error)`. In one transaction it parses the file, inserts with `ON CONFLICT (bank_account_id, dedupe_key) DO NOTHING`, records the `bank_imports` row and audits.
- **Routes**, all under `/api/v1/orgs/{org}`:
  - `POST /bank-accounts` takes `{name, preset?, csv_mapping?}`, with exactly one of `preset` or `csv_mapping`. Needs `PermReconWrite`.
  - `GET /bank-accounts` needs `PermReportsRead`.
  - `POST /bank-accounts/{id}/imports` is multipart with field `file` and a 5 MiB limit, returning `{rows_total, rows_new}`. Needs `PermReconWrite`.
  - `GET /bank-lines?account_id=&status=&from=&to=` needs `PermReportsRead`.

- [ ] **Step 1: Write failing unit tests (`csv_test.go`).**
  - The CommBank preset on a 3-row fixture gives the right dates, amounts `-4550`, `120000` and `-1299`, and trimmed descriptions.
  - A debit/credit mapping where "12.30" in the debit column gives `-1230`.
  - Each amount format above parses correctly. "12.345" fails, and the error names the row.
  - `DedupeKeys` on two identical triples gives 2 distinct keys. The same input run again gives the same keys.
  - `Validate` rejects both styles set together, and rejects neither style.
- [ ] **Step 2: Write failing integration tests.**
  - `TestImportIdempotent`: importing the same file twice gives `RowsNew` of 3, then 0.
  - `TestImportOverlap`: an overlapping second file adds only the new rows.
  - `TestImportKeepsSameDayDuplicates`.
  - `TestImportCrossOrgAccount404`.
  - `TestImportTooLarge413` (a body over 5 MiB gives 413).
  - `TestReconModuleDisabled404`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: add bank accounts and idempotent csv import`

### Task 3: Categories, expenses and receipts

**Files:**
- Create:
  - `internal/db/migrations/0009_expenses.sql`
  - `internal/recon/categories.go`, `expenses.go` and `files.go`
  - `internal/recon/expenses_integration_test.go`
- Modify:
  - `internal/recon/routes.go`
  - `internal/core/orgs.go`: `CreateOrg` seeds the default categories.

**`0009_expenses.sql`:**
- `ALTER TABLE categories ADD COLUMN capital boolean NOT NULL DEFAULT false`.
- Seed the default **expense** categories for every existing org with `INSERT ... ON CONFLICT (org_id, name) DO NOTHING`, using the list below.
- `files`:
  - `id`, `org_id NOT NULL`, `sha256 bytea NOT NULL`, `mime text NOT NULL`, `size_bytes int NOT NULL`, `original_name text NOT NULL`, `created_at`.
  - `UNIQUE(org_id, sha256)`.
- `expenses`:
  - `id`, `org_id NOT NULL`, `spent_on date NOT NULL`, `supplier_contact_id NULL REFERENCES contacts`, `description text NOT NULL`.
  - `receipt_file_id NULL REFERENCES files`, `bank_line_id NULL UNIQUE REFERENCES bank_lines`, `created_at`.
- `expense_lines`:
  - `id`, `org_id NOT NULL`, `expense_id NOT NULL REFERENCES expenses ON DELETE CASCADE`, `category_id NOT NULL REFERENCES categories`.
  - `amount bigint NOT NULL CHECK (amount > 0)`, `gst_code text NOT NULL`, `gst_amount bigint NOT NULL`.
  - `property_id NULL REFERENCES properties`, `project_id NULL REFERENCES projects`.
  - `CHECK (property_id IS NULL OR project_id IS NULL)`.

**Default expense categories** (name → default GST code, capital):

| Name | GST code | Capital |
|---|---|---|
| Repairs & maintenance | GST | |
| Insurance | GST | |
| Council rates | N-T | |
| Water | FRE | |
| Strata / body corporate | GST | |
| Agent & advertising fees | GST | |
| Loan interest | INP | |
| Bank fees | INP | |
| Stripe fees | GST | |
| Software & subscriptions | GST | |
| Accounting & legal | GST | |
| Travel | GST | |
| Office & supplies | GST | |
| Equipment | GST | capital |
| Other expenses | GST | |

The defaults are editable.

**Interfaces:**
- Produces:
  - **Categories:** `ListCategories`, `CreateCategory` and `UpdateCategory`, using the strict PUT with `{name, type, default_gst_code, capital}`.
  - **Files:**
    - `SaveFile(ctx, q, orgID, filesDir, name string, r io.Reader) (File, error)`.
    - The limit is 10 MiB.
    - The MIME type comes from `http.DetectContentType` on the first 512 bytes, and only `application/pdf`, `image/jpeg` and `image/png` are allowed. Anything else gives 422.
    - The file is written to `<filesDir>/<org_id>/<hex sha256>` with mode 0600, and directories are created with 0700.
    - The same content uploaded again returns the existing row.
  - **Expenses:**
    - `type NewExpense struct{ SpentOn time.Time; SupplierContactID *int64; Description string; ReceiptFileID *int64; Lines []NewExpenseLine }`.
    - `type NewExpenseLine struct{ CategoryID int64; Amount core.Cents; GSTCode *core.GSTCode; PropertyID, ProjectID *int64 }`.
    - A nil `GSTCode` means the category default.
  - `CreateExpense(ctx, q, orgID, NewExpense) (Expense, []string /*warnings*/, error)`:
    - It needs at least one line.
    - Every referenced ID must belong to the org.
    - A line with a residential property is forced to `INP`, and the warning "residential property expenses are input-taxed; GST credit not claimed" is added.
    - In an unregistered org, `gst_amount` is 0.
    - Otherwise `gst_amount` is `GSTInInclusive`.
  - `GetExpense`, `ListExpenses(filter{From, To, CategoryID, PropertyID, ProjectID, Unlinked bool})` and `DeleteExpense`. `DeleteExpense` gives 409 if the expense is linked to a bank line.
  - **Routes**, all under `/api/v1/orgs/{org}`, with writes needing `PermReconWrite` and reads needing `PermReportsRead`:
    - `GET/POST /categories` and `PUT /categories/{id}`.
    - `POST /files` (multipart field `file`) and `GET /files/{id}`, which streams with the stored MIME type and `Content-Disposition: attachment`.
    - `POST/GET /expenses`, `GET /expenses/{id}` and `DELETE /expenses/{id}`.

- [ ] **Step 1: Write failing integration tests.**
  - `TestCreateOrgSeedsCategories`: 15 rows, with "Equipment" capital.
  - `TestExpenseSplitLinesGST`: a $110.00 Repairs line gives GST 1000. A $55.00 Water line gives GST 0. The expense total is 16500.
  - `TestExpenseResidentialForcedINP`: a Repairs line tagged to a residential property is stored as `INP` with GST 0, plus the warning.
  - `TestExpenseUnregisteredOrgNoGST`.
  - `TestExpenseCrossOrgCategory422`.
  - `TestFileUploadSniffAndDedupe`: a PNG upload succeeds, uploading the same bytes again returns the same ID, and a text file gives 422.
  - `TestFileDownloadOtherOrg404`.
  - `TestDeleteLinkedExpense409`: set `bank_line_id` directly in SQL first.
- [ ] **Step 2: Run them and confirm FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: add categories, split expenses and receipt files`

### Task 4: Matching bank lines

**Files:**
- Create: `internal/recon/match.go` and `internal/recon/match_integration_test.go`
- Modify: `internal/recon/routes.go`

**Interfaces:**
- Consumes: `core.RecordPayment`, `core.GetInvoice`, `billing.ActorFrom`, `billing.InvoiceJSON`, and the Task 3 expenses.
- Produces. Every action takes `SELECT ... FROM bank_lines WHERE org_id AND id FOR UPDATE` and requires status `unmatched`, otherwise `ErrInvalidState` (409). Every action audits.
  - `MatchInvoice(ctx, tx, orgID, lineID, invoiceID int64, method string) (core.Payment, error)`:
    - The line must be a credit (amount > 0).
    - `method` is `bank_transfer` or `payid`.
    - It records a payment with amount = line amount, `paid_on` = `posted_on` and `bank_line_id`, then sets status `matched`.
    - An amount above or below the outstanding balance is allowed. Overpayment is stored as-is, per Plan 1.
  - `MatchExpense(ctx, tx, orgID, lineID, expenseID int64) error`:
    - The line must be a debit, and `-line.amount` must equal the expense total, otherwise 422.
    - The expense must not already be linked.
    - It sets `expenses.bank_line_id` and status `matched`.
  - `CreateExpenseFromLine(ctx, tx, orgID, lineID int64, in NewExpense) (Expense, []string, error)`:
    - `SpentOn` defaults to `posted_on`.
    - The total must equal `-line.amount`.
    - It then links the expense like `MatchExpense`.
  - `Ignore(ctx, tx, orgID, lineID int64, reason string) error`: `reason` must be one of `transfer`, `drawings`, `bond_lodgement`, `bond_refund` or `other`.
  - `Unmatch(ctx, tx, orgID, lineID int64) error`:
    - The line must not be `unmatched`.
    - Delete payments with this `bank_line_id`. If the invoice was `paid` and its remaining payment sum is now below the total, set it back to `open`.
    - Clear `expenses.bank_line_id`.
    - Clear the ignore reason.
    - Set status `unmatched`.
    - Payout links are handled in Task 5.
  - `Suggest(ctx, q, orgID, lineID int64) ([]Suggestion, error)`, where `Suggestion{Kind "invoice"|"expense", ID int64, Score int, Reason string}`:
    - For credits: open invoices with outstanding equal to the amount. The score is 100 if the description contains the invoice's `payment_reference`, case-insensitive. Otherwise it is 50 minus the number of days between `issue_date` and `posted_on`, floored at 0.
    - For debits: unlinked expenses whose total equals the amount and whose `spent_on` is within ±7 days. The score is 50 minus the day difference.
    - Results are sorted by score, highest first, with a maximum of 10.
  - `AutoMatch(ctx, pool, orgID) (matched int, err error)`:
    - For each unmatched credit line, look for exactly one open invoice whose `payment_reference` appears in the description and whose outstanding equals the amount. Then call `MatchInvoice` with method `bank_transfer`.
    - Each line gets its own transaction. Ambiguous lines are skipped.
- **Routes**, under `/api/v1/orgs/{org}/bank-lines/{id}`, all needing `PermReconWrite` except where noted:
  - `POST /match-invoice` with `{invoice_id, method}`.
  - `POST /match-expense` with `{expense_id}`.
  - `POST /expense` with the `NewExpense` JSON.
  - `POST /ignore` with `{reason}`.
  - `POST /unmatch`.
  - `GET /suggestions`, which needs `PermReportsRead`.
  - `POST /api/v1/orgs/{org}/reconcile/auto`.

- [ ] **Step 1: Write failing integration tests.**
  - `TestMatchInvoicePaysAndUnmatchReverts`: invoice total 50000 and credit line 50000 gives paid. Unmatch sets it back to open and leaves 0 payments.
  - `TestMatchInvoiceConcurrent`: two goroutines match the same line. One succeeds, the other gets 409, and 1 payment exists.
  - `TestMatchInvoiceRejectsDebit422`.
  - `TestCreateExpenseFromLineTotalMustMatch`: a line of -16500 with lines totalling 16400 gives 422. Totalling 16500 succeeds and gives status matched.
  - `TestSuggestRanksPaymentReference`: two open invoices of the same amount. The one whose reference is in the description scores 100 and comes first.
  - `TestAutoMatchOnlyUnambiguous`: one clean match, and one line whose amount fits two invoices with no reference, gives `matched == 1`.
  - `TestIgnoreReasonValidated`.
- [ ] **Step 2: Run them and confirm FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: add bank line matching, suggestions and auto-match`

### Task 5: Stripe payouts and fees

**Files:**
- Create: `internal/db/migrations/0010_payouts.sql`, `internal/recon/payouts.go` and `internal/recon/payouts_integration_test.go`
- Modify:
  - `internal/payments/provider.go`
  - `internal/payments/fake.go`
  - `internal/payments/stripe/stripe.go` and `stripe_test.go`
  - `internal/recon/match.go`: `Unmatch` also reverses payout links.
  - `internal/recon/routes.go`

**`0010_payouts.sql`:**
- `payouts`:
  - `id`, `org_id NOT NULL`, `provider_ref text NOT NULL`, `amount bigint NOT NULL`, `arrival_on date NOT NULL`.
  - `bank_line_id bigint NOT NULL UNIQUE REFERENCES bank_lines`, `fee_expense_id bigint NULL REFERENCES expenses`.
  - `UNIQUE(org_id, provider_ref)`.
- `ALTER TABLE payments ADD COLUMN payout_id bigint NULL REFERENCES payouts`.

**Interfaces:**
- Produces:

```go
// payments.Provider gains:
GetPayout(ctx context.Context, payoutRef string) (Payout, error)
ListPayouts(ctx context.Context, arrivalFrom, arrivalTo time.Time) ([]Payout, error)
PayoutTransactions(ctx context.Context, payoutRef string) ([]BalanceTxn, error)
type Payout struct{ Ref string; Amount core.Cents; ArrivalOn time.Time; Status string }
type BalanceTxn struct{ Ref, Type, PaymentRef string; Amount, Fee core.Cents } // Type: "charge"|"payment"|"refund"|"stripe_fee"|"adjustment"|...; PaymentRef = payment intent id for charges
```

- **Stripe implementation:**
  - `GetPayout` and `ListPayouts` use the payouts API, with `arrival_date` in `loc`.
  - `PayoutTransactions` lists balance transactions filtered by `payout` and expands `source` to reach the payment intent ID.
  - All calls use the detached 2-minute context pattern.
- **Fake:** settable payouts and transactions.
- `MatchPayout(ctx, pool, b *billing.Service, orgID int64, orgSlug string, lineID int64, payoutRef string) (PayoutResult{PaymentsLinked int; Fees core.Cents; FeeExpenseID *int64}, error)`. The Stripe calls happen **before** the transaction. Inside one transaction:
  1. Lock the line, which must be an unmatched credit.
  2. `payout.Amount` must equal `line.amount`, and `ArrivalOn` must be within ±5 days of `posted_on`. Otherwise 409.
  3. Every `charge` or `payment` transaction must map to an existing org payment by `provider_ref = PaymentRef`. Any other type except `stripe_fee`, or any unknown charge, gives 409 listing the refs, and nothing is written.
  4. Set `payments.payout_id` and `bank_line_id` on those payments.
  5. Fees are `Σ txn.Fee` plus the absolute value of any `stripe_fee` transactions. If fees are above 0, create an expense dated `posted_on`, described "Stripe fees <payout ref>", with one line in the "Stripe fees" category, code `GST`, amount = fees. The amount includes GST, because Stripe AU fees include GST. Link it as `fee_expense_id`, not through `bank_line_id`, because the bank line already belongs to the payout.
  6. Insert the payout and set the line to `matched`.
- `Unmatch` on a payout line clears `payments.payout_id` and `bank_line_id` (it does not delete the payments, which came from webhooks), deletes the fee expense, and deletes the payout row.
- `Suggest` on a credit line also returns `Kind "payout"` entries from `ListPayouts` (arrival ±5 days, amount equal) at score 90. If the org has no provider, this is skipped silently.
- Route: `POST /bank-lines/{id}/match-payout` with `{payout_ref}`.

- [ ] **Step 1: Write failing tests.**
  - `stripe_test.go`: `TestPayoutTransactionsMapsPaymentIntent` uses an httptest stub with a charge whose expanded source is a payment intent, giving the right `PaymentRef` and `Fee`.
  - Integration, using the Fake:
    - `TestMatchPayoutLinksPaymentsAndBooksFees`: two webhook payments (pi_1 and pi_2) with fees 110 and 220. A payout with net = Σ amount − 330 and a line of the same amount give `PaymentsLinked == 2` and a fee expense of 330 with GST 30. Both payments keep their original `paid_on` (cash basis).
    - `TestMatchPayoutAmountMismatch409NoWrites`.
    - `TestMatchPayoutUnknownCharge409`.
    - `TestUnmatchPayoutReverses`.
- [ ] **Step 2: Run them and confirm FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run all suites. Expected: PASS.**
- [ ] **Step 5: Commit:** `feat: match stripe payouts and book fees as expenses`

### Task 6: P&L, BAS and CSV export

**Files:**
- Create:
  - `internal/reports/allocate.go` and `allocate_test.go`
  - `internal/reports/pnl.go`, `bas.go` and `periods.go` (+ `periods_test.go`)
  - `internal/reports/routes.go`
  - `internal/reports/reports_integration_test.go`
- Modify: `internal/httpapi/server.go`. Mount the reports routes as core: always on and not a toggleable module.

**Interfaces:**
- Produces:
  - **Pure allocation:**
    - `func Allocate(payment core.Cents, lines []AllocLine) []Share`.
    - `AllocLine{Amount, GST core.Cents; Code core.GSTCode; PropertyID, ProjectID *int64}`, where `Amount` is ex-GST.
    - `Share{Line AllocLine; Incl, GST, Ex core.Cents}`.
    - It follows the Global Constraints allocation rule exactly.
  - **Pure period:** `func FYQuarter(s string, fyStartMonth int) (from, to time.Time, err error)` parses `FY2027-Q1` and similar.
  - **Rows:** `func IncomeRows(ctx, q, orgID, from, to) ([]IncomeRow, error)`:
    - Payments with `paid_on` in `[from, to]` on invoices whose status is not draft or void and whose source is not `bond`.
    - Each is allocated over its invoice's lines.
    - `IncomeRow{PaymentID, InvoiceID, InvoiceNumber, PaidOn, Category, Code, Incl, GST, Ex, PropertyID, ProjectID}`.
  - **Rows:** `func ExpenseRows(ctx, q, orgID, from, to) ([]ExpenseRow, error)`:
    - Expense lines whose `spent_on` is in range.
    - `ExpenseRow{ExpenseID, LineID, SpentOn, Category, Capital bool, Code, Incl, GST, Ex, PropertyID, ProjectID}`.
  - `func PnL(income []IncomeRow, exp []ExpenseRow, groupBy string) []PnLGroup`:
    - `groupBy` is `org`, `property` or `project`.
    - `PnLGroup{Key string; Income, Expenses map[string]core.Cents /*by category, ex-GST*/; TotalIncome, TotalExpenses, Net core.Cents}`.
    - Rows with no property or project go in group "Unassigned".
  - `func BAS(income []IncomeRow, exp []ExpenseRow) BASFigures`, where `BASFigures{G1, G2, G3, G4, G10, G11, A1, B1 core.Cents}` is cents and `Dollars()` truncates each figure:
    - G1 = Σ Incl over income with codes `GST`, `FRE` and `INP`.
    - G2 = 0, because exports are not tracked.
    - G3 = Σ Incl over `FRE` income.
    - G4 = Σ Incl over `INP` income.
    - G10 = Σ Incl over capital expense lines whose code is not `N-T`.
    - G11 = Σ Incl over non-capital expense lines whose code is not `N-T`.
    - 1A = Σ GST over income.
    - 1B = Σ GST over expense lines.
  - **Routes**, under `/api/v1/orgs/{org}/reports`, all needing `PermReportsRead`:
    - `GET /pnl?from=&to=&group_by=`.
    - `GET /bas?period=FY2027-Q1` (or `from`/`to`). Returns 422 if the org is not GST-registered. Returns `{period, figures_cents, figures_dollars}`.
    - `GET /bas/detail?period=&field=1A|1B|G1|G3|G4|G10|G11` returns the exact rows summed for that field.
    - `GET /transactions.csv?from=&to=` gives one row per income or expense row, with columns `date,type,category,gst_code,incl_gst,gst,ex_gst,property,project,reference`. Amounts are in dollars with 2 decimals.
    - `GET /pnl.csv` and `GET /bas.csv`.
    - The CSV responses use `Content-Type: text/csv` and `Content-Disposition: attachment; filename=...`.

- [ ] **Step 1: Write failing pure tests.**
  - `Allocate(5000, [{Amount 10000, GST 1000, GST}, {Amount 4000, GST 0, FRE}])`. The invoice total is 15000. The shares are 3667 (GST `DivRound(3667×1000, 11000)` = 333), then 1333 as the remainder (GST 0). The shares sum to 5000.
  - `Allocate` of the full total gives the exact line totals.
  - `FYQuarter("FY2027-Q1", 7)` gives 2026-07-01 to 2026-09-30. `FY2027-Q3` gives 2027-01-01 to 2027-03-31. A bad string gives an error.
- [ ] **Step 2: Write failing integration tests.**
  - `TestBASTiesToDetail`, in a GST-registered org:
    - Rent from a residential property ($500, `INP`) paid in full.
    - A consulting invoice (10000 + 1000 GST) paid in two halves inside the quarter.
    - A bond invoice paid, which must be excluded.
    - Expense lines: Repairs $110 `GST` and Equipment $2200 `GST` (capital).
    - Expected: G1 = 50000 + 11000, G4 = 50000, 1A = 1000, G10 = 220000, G11 = 11000, 1B = 1000 + 20000.
    - For each field, the sum of the `/bas/detail` rows equals the figure.
  - `TestPnLByProperty`: rent and a repair on property P1, and consulting with no property, give groups "P1" and "Unassigned" with the right nets.
  - `TestBASUnregistered422`.
  - `TestTransactionsCSV`: the header row and row count are right, and amounts are formatted "110.00".
  - `TestReportsExcludeDraftAndVoid`.
- [ ] **Step 3: Run them and confirm FAIL.**
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run all suites. Expected: PASS.**
- [ ] **Step 6: Commit:** `feat: add cash-basis p&l, bas with drill-down and csv export`

---

## Done for Plan 4

- All suites pass.
- On `docker compose up`:
  1. Create a bank account with the CommBank preset and import a CSV.
  2. Auto-match the bank transfers.
  3. Match a Stripe payout. Use Stripe test mode, or the Fake in tests.
  4. Turn the remaining debits into expenses with receipts.
  5. `GET /reports/bas?period=` gives figures whose drill-down rows sum exactly.
  6. Download `transactions.csv` for the accountant.
- Update the follow-ups doc. Credit notes are deferred and the Plan 4 items are done.
