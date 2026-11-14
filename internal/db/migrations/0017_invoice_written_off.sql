-- written_off records that an invoice was marked uncollectible, so a payment
-- reversal on a later-paid invoice restores 'uncollectible' instead of
-- silently reinstating the debt as 'open'.
ALTER TABLE invoices ADD COLUMN written_off boolean NOT NULL DEFAULT false;
