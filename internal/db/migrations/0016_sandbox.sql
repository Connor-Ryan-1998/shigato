-- Explicit ownership is required before populate-sbx may replace any data.
-- Slugs, names and email addresses are editable and are not ownership markers.
CREATE TABLE sandbox_orgs (
    org_id bigint PRIMARY KEY REFERENCES orgs
);

CREATE TABLE sandbox_users (
    user_id bigint PRIMARY KEY REFERENCES users
);
