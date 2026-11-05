-- Opening arrears become a real draft invoice created with the lease, so
-- the ledger can track what's still outstanding on it (and payments
-- against it) instead of a static figure.
ALTER TABLE leases
    ADD COLUMN opening_arrears_invoice_id bigint REFERENCES invoices;
