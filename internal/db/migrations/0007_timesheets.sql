-- Timesheets module: billable projects, their tasks, and logged time
-- entries. A project belongs to one client contact; a task optionally
-- overrides the project's hourly rate. Time is logged against a task and
-- billed once linked to an invoice line (invoice_lines.project_id, added in
-- 0001, gets its FK here now that projects exists).
CREATE TABLE projects (
    id                  bigserial PRIMARY KEY,
    org_id              bigint NOT NULL REFERENCES orgs,
    client_contact_id   bigint NOT NULL REFERENCES contacts,
    name                text NOT NULL,
    hourly_rate         bigint NOT NULL CHECK (hourly_rate > 0),
    gst_code            text NOT NULL DEFAULT 'GST' CHECK (gst_code IN ('GST', 'FRE')),
    active              boolean NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE TABLE project_tasks (
    id              bigserial PRIMARY KEY,
    org_id          bigint NOT NULL REFERENCES orgs,
    project_id      bigint NOT NULL REFERENCES projects,
    name            text NOT NULL,
    hourly_rate     bigint CHECK (hourly_rate > 0),
    active          boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE TABLE time_entries (
    id                  bigserial PRIMARY KEY,
    org_id              bigint NOT NULL REFERENCES orgs,
    task_id             bigint NOT NULL REFERENCES project_tasks,
    user_id             bigint NOT NULL REFERENCES users,
    work_date           date NOT NULL,
    minutes             int NOT NULL CHECK (minutes BETWEEN 1 AND 1440),
    note                text NOT NULL DEFAULT '',
    invoice_line_id     bigint REFERENCES invoice_lines,
    created_at          timestamptz NOT NULL DEFAULT now()
);

-- Finds an entry's lease-period-equivalent "still unbilled" rows fast: the
-- billing run scans unbilled entries for a task/date range.
CREATE INDEX time_entries_unbilled ON time_entries (org_id, task_id, work_date) WHERE invoice_line_id IS NULL;

ALTER TABLE invoice_lines
    ADD CONSTRAINT invoice_lines_project_fk FOREIGN KEY (project_id) REFERENCES projects (id);
