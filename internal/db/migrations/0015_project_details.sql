-- Project details: a lifecycle status (replacing the bare active flag), a
-- description, an optional start/end date and a billing method. Fixed-price
-- projects still track time but are skipped by the timesheet billing run;
-- their fee is invoiced manually.
--
-- active becomes a generated column so every existing reader (time logging,
-- the active-only project list, the billing run) keeps working unchanged:
-- pending and in-progress projects accept time, completed and cancelled ones
-- don't.
ALTER TABLE projects
    ADD COLUMN status text NOT NULL DEFAULT 'in_progress'
        CHECK (status IN ('pending', 'in_progress', 'completed', 'cancelled')),
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN start_date date,
    ADD COLUMN end_date date,
    ADD COLUMN billing_method text NOT NULL DEFAULT 'tm' CHECK (billing_method IN ('fixed', 'tm')),
    ADD CONSTRAINT projects_date_order CHECK (start_date IS NULL OR end_date IS NULL OR end_date >= start_date);

UPDATE projects SET status = 'completed' WHERE NOT active;

ALTER TABLE projects DROP COLUMN active;
ALTER TABLE projects ADD COLUMN active boolean
    GENERATED ALWAYS AS (status IN ('pending', 'in_progress')) STORED;
