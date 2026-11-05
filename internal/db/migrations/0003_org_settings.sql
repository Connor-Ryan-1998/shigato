-- Rent invoices need a configurable lead time (how many days before the
-- period start to issue them) and an opt-in to auto-issuing, both editable
-- through the org settings API.
ALTER TABLE orgs
    ADD COLUMN invoice_lead_days smallint NOT NULL DEFAULT 7 CHECK (invoice_lead_days BETWEEN 0 AND 14),
    ADD COLUMN auto_send_rent boolean NOT NULL DEFAULT false;
