-- Invoices need to remember the requested payment term from draft creation
-- until they are issued, since IssueInvoice computes due_date from the
-- issue date (today) plus this term, and there is otherwise nowhere to
-- store it between those two steps.
ALTER TABLE invoices ADD COLUMN due_in_days int NOT NULL DEFAULT 0;
