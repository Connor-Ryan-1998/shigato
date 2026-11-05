-- A rent invoice is due on its period's start date, not a fixed number of
-- days after whenever it happens to be issued (which can be days after the
-- draft was created, e.g. a failed auto-issue retried manually). due_on
-- lets a caller pin that fixed due date; due_in_days still drives it for
-- every other invoice (and keeps the Stripe integration's existing term).
ALTER TABLE invoices
    ADD COLUMN due_on date;
