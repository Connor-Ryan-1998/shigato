-- Review follow-up on 0009_expenses.sql: a database-level backstop for
-- expense_lines.gst_code, matching the CHECK Postgres can't express on
-- categories.default_gst_code without also forbidding it from ever being
-- NULL (allowed: a category created before core.ParseGSTCode validation
-- applied to it). The application (recon.CreateExpense) already rejects an
-- empty/invalid code before insert; this constraint guards against any
-- other write path.
ALTER TABLE expense_lines ADD CONSTRAINT expense_lines_gst_code_check
    CHECK (gst_code IN ('GST', 'FRE', 'INP', 'N-T'));
