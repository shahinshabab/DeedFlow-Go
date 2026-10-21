package store

import (
	"context"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

var (
	orgCols        = db.Columns[models.Organization]("")
	membershipCols = db.Columns[models.Membership]("")
	credCols       = db.Columns[models.PortalCredential]("")
)

// MembershipOf is the user's membership, or nil.
func (s *Store) MembershipOf(ctx context.Context, userID int64) (*models.Membership, error) {
	m, err := db.One[models.Membership](ctx, s.q(), `SELECT `+membershipCols+` FROM accounts_membership WHERE user_id = $1`, userID)
	if err == db.ErrNotFound {
		return nil, nil
	}
	return m, err
}

// OrganizationByID loads an organization, or nil.
func (s *Store) OrganizationByID(ctx context.Context, id int64) (*models.Organization, error) {
	o, err := db.One[models.Organization](ctx, s.q(), `SELECT `+orgCols+` FROM accounts_organization WHERE id = $1`, id)
	if err == db.ErrNotFound {
		return nil, nil
	}
	return o, err
}

// CreateOrganization inserts an organization.
func (s *Store) CreateOrganization(ctx context.Context, o *models.Organization) error {
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now()
	}
	_, err := db.Insert(ctx, s.q(), "accounts_organization", o)
	return err
}

// SaveOrganization writes an organization's name.
func (s *Store) SaveOrganization(ctx context.Context, o *models.Organization) error {
	return db.Update(ctx, s.q(), "accounts_organization", o, "name")
}

// CreateMembership inserts a membership.
func (s *Store) CreateMembership(ctx context.Context, m *models.Membership) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now()
	}
	_, err := db.Insert(ctx, s.q(), "accounts_membership", m)
	return err
}

// OrganizationAdmin is the organization's admin user, or nil.
func (s *Store) OrganizationAdmin(ctx context.Context, orgID int64) (*models.User, error) {
	u, err := db.One[models.User](ctx, s.q(), `SELECT `+db.Columns[models.User]("u")+` FROM auth_user u
		JOIN accounts_membership m ON m.user_id = u.id WHERE m.organization_id = $1 AND m.role = 'admin' LIMIT 1`, orgID)
	if err == db.ErrNotFound {
		return nil, nil
	}
	return u, err
}

// MemberRow is a membership with its user, for the organization page.
type MemberRow struct {
	Membership models.Membership
	User       models.User
}

// Members lists an organization's members, admin first.
func (s *Store) Members(ctx context.Context, orgID int64) ([]MemberRow, error) {
	rows, err := s.q().Query(ctx, `SELECT `+db.Columns[models.Membership]("m")+`, `+db.Columns[models.User]("u")+`
		FROM accounts_membership m JOIN auth_user u ON u.id = m.user_id WHERE m.organization_id = $1
		ORDER BY m.role DESC, u.first_name, u.username`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemberRow
	for rows.Next() {
		var r MemberRow
		var ptrs []any
		ptrs = append(ptrs, structPtrs(&r.Membership)...)
		ptrs = append(ptrs, structPtrs(&r.User)...)
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StaffRow is one member of an organization with their user.
type StaffRow = MemberRow

// StaffMember is a staff membership of the organization (never the admin).
func (s *Store) StaffMember(ctx context.Context, orgID, userID int64) (*MemberRow, error) {
	rows, err := s.Members(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].User.ID == userID && rows[i].Membership.Role == models.RoleStaff {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// PortalCredentialFor is the organization's portal login, or nil.
func (s *Store) PortalCredentialFor(ctx context.Context, orgID int64) (*models.PortalCredential, error) {
	c, err := db.One[models.PortalCredential](ctx, s.q(), `SELECT `+credCols+` FROM accounts_portalcredential WHERE organization_id = $1`, orgID)
	if err == db.ErrNotFound {
		return nil, nil
	}
	return c, err
}

// SavePortalCredential inserts or updates the organization's portal login.
func (s *Store) SavePortalCredential(ctx context.Context, c *models.PortalCredential) error {
	c.UpdatedAt = now()
	if c.ID == 0 {
		_, err := db.Insert(ctx, s.q(), "accounts_portalcredential", c)
		return err
	}
	return db.Update(ctx, s.q(), "accounts_portalcredential", c)
}

// CredentialsCollectingReceipts lists portal logins that have nightly receipt collection on.
func (s *Store) CredentialsCollectingReceipts(ctx context.Context) ([]models.PortalCredential, error) {
	return db.All[models.PortalCredential](ctx, s.q(), `SELECT `+credCols+` FROM accounts_portalcredential
		WHERE collect_receipts AND portal_username <> ''`)
}

// ListOrganizations returns all organizations for the admin.
func (s *Store) ListOrganizations(ctx context.Context) ([]models.Organization, error) {
	return db.All[models.Organization](ctx, s.q(), `SELECT `+orgCols+` FROM accounts_organization ORDER BY name`)
}

// CreateStaff makes a staff user and their membership in one transaction.
func (s *Store) CreateStaff(ctx context.Context, u *models.User, orgID int64) error {
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.CreateUser(ctx, u); err != nil {
			return err
		}
		return tx.CreateMembership(ctx, &models.Membership{UserID: u.ID, OrganizationID: orgID, Role: models.RoleStaff})
	})
}

// CountDeedsOf counts an organization's deeds.
func (s *Store) CountDeedsOf(ctx context.Context, orgID int64) (int, error) {
	var n int
	err := s.q().QueryRow(ctx, `SELECT count(*) FROM deeds_deed WHERE organization_id = $1`, orgID).Scan(&n)
	return n, err
}
