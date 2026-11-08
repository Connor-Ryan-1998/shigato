package core

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Contact is a tenant, client or supplier an org deals with.
type Contact struct {
	ID                  int64
	OrgID               int64
	Kind                string
	Name                string
	Email               string
	Phone               string
	CompanyName         string
	ABN                 string
	ProviderCustomerRef string
}

const contactColumns = `id, org_id, kind, name, coalesce(email,''), coalesce(phone,''),
	coalesce(company_name,''), coalesce(abn,''), coalesce(provider_customer_ref,'')`

func scanContact(row pgx.Row) (Contact, error) {
	var c Contact
	err := row.Scan(&c.ID, &c.OrgID, &c.Kind, &c.Name, &c.Email, &c.Phone,
		&c.CompanyName, &c.ABN, &c.ProviderCustomerRef)
	return c, err
}

// CreateContact inserts a new contact for orgID and returns it.
func CreateContact(ctx context.Context, q Querier, orgID int64, c Contact) (Contact, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO contacts (org_id, kind, name, email, phone, company_name, abn, provider_customer_ref)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''))
		RETURNING `+contactColumns,
		orgID, c.Kind, c.Name, c.Email, c.Phone, c.CompanyName, c.ABN, c.ProviderCustomerRef,
	)
	return scanContact(row)
}

// GetContact looks up a contact by id, scoped to orgID. It returns
// ErrNotFound if no such contact exists in that org.
func GetContact(ctx context.Context, q Querier, orgID, id int64) (Contact, error) {
	row := q.QueryRow(ctx, `SELECT `+contactColumns+` FROM contacts WHERE org_id = $1 AND id = $2`, orgID, id)
	c, err := scanContact(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Contact{}, ErrNotFound
	}
	return c, err
}

// UpdateContact replaces profile details without changing kind or provider identity.
func UpdateContact(ctx context.Context, q Querier, orgID, id int64, c Contact) (Contact, error) {
	updated, err := scanContact(q.QueryRow(ctx, `
		UPDATE contacts SET name=$3, email=NULLIF($4,''), phone=NULLIF($5,''),
			company_name=NULLIF($6,''), abn=NULLIF($7,'')
		WHERE org_id=$1 AND id=$2
		RETURNING `+contactColumns, orgID, id, c.Name, c.Email, c.Phone, c.CompanyName, c.ABN))
	if errors.Is(err, pgx.ErrNoRows) {
		return Contact{}, ErrNotFound
	}
	return updated, err
}

// ListContacts lists an org's contacts, optionally filtered by kind. An
// empty kind returns every contact.
func ListContacts(ctx context.Context, q Querier, orgID int64, kind string) ([]Contact, error) {
	var rows pgx.Rows
	var err error
	if kind == "" {
		rows, err = q.Query(ctx, `SELECT `+contactColumns+` FROM contacts WHERE org_id = $1 ORDER BY id`, orgID)
	} else {
		rows, err = q.Query(ctx, `SELECT `+contactColumns+` FROM contacts WHERE org_id = $1 AND kind = $2 ORDER BY id`, orgID, kind)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contacts := []Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}
