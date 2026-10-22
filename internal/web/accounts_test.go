package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/portalcrypt"
)

// Ports of accounts/tests.py (RoleTests).

type roleFixture struct {
	*site
	org, otherOrg          *models.Organization
	admin, staff           *models.User
	otherAdmin, otherStaff *models.User
}

func newRoleFixture(t *testing.T) *roleFixture {
	s := newSite(t)
	f := &roleFixture{site: s}
	f.org = s.org("Office A")
	f.admin = s.member("admina", f.org, models.RoleAdmin)
	f.staff = s.member("staffa", f.org, models.RoleStaff)
	enc, err := portalcrypt.Encrypt(s.cfg.FieldEncryptionKey, "secret-a")
	if err != nil {
		t.Fatal(err)
	}
	cred := &models.PortalCredential{OrganizationID: f.org.ID, PortalUsername: "portal-a", PortalPassword: enc, NewDeedMenuIndex: 1, MyDeedsMenuIndex: 2}
	if err := s.st.SavePortalCredential(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	f.otherOrg = s.org("Office B")
	f.otherAdmin = s.member("adminb", f.otherOrg, models.RoleAdmin)
	f.otherStaff = s.member("staffb", f.otherOrg, models.RoleStaff)
	return f
}

func TestStaffSeesAndEditsTheirOrganizationsDeedsOnly(t *testing.T) {
	f := newRoleFixture(t)
	mine, theirs := f.deed(f.admin), f.deed(f.otherAdmin)
	if mine.OrganizationID == nil || *mine.OrganizationID != f.org.ID {
		t.Fatalf("a deed takes its owner's organization, got %v", mine.OrganizationID)
	}
	c := f.client().login(f.staff)
	listing := c.get(f.rev("deeds:my_deeds"))
	if !listing.contains(fmt.Sprintf("Deed #%d", mine.ID)) || listing.contains(fmt.Sprintf("Deed #%d", theirs.ID)) {
		t.Errorf("the list should show only the organization's deeds")
	}
	c.wantStatus(f.rev("deeds:detail", mine.PublicID), 200)
	c.wantStatus(f.rev("deeds:wizard", mine.PublicID), 200)
	c.wantStatus(f.rev("deeds:detail", theirs.PublicID), 404)
	c.wantStatus(f.rev("deeds:wizard", theirs.PublicID), 404)
}

func TestStaffNewDeedBelongsToTheOrganization(t *testing.T) {
	f := newRoleFixture(t)
	c := f.client().login(f.staff)
	c.get(f.rev("deeds:create"))
	var orgID *int64
	var portalUser string
	err := f.db.Pool.QueryRow(context.Background(), `SELECT d.organization_id, COALESCE(p.portal_username, '')
		FROM deeds_deed d LEFT JOIN accounts_portalcredential p ON p.organization_id = d.organization_id
		WHERE d.owner_id = $1`, f.staff.ID).Scan(&orgID, &portalUser)
	if err != nil {
		t.Fatal(err)
	}
	if orgID == nil || *orgID != f.org.ID || portalUser != "portal-a" {
		t.Errorf("new deed: org %v, portal login %q", orgID, portalUser)
	}
}

func TestStaffCannotOpenTestAreaOrOrganizationPage(t *testing.T) {
	f := newRoleFixture(t)
	c := f.client().login(f.staff)
	for _, name := range []string{"deeds:test_dashboard", "deeds:pdf_test", "deeds:ai_test", "deeds:selenium_test", "accounts:organization"} {
		if r := c.get(f.rev(name)); r.Status != 403 {
			t.Errorf("%s = %d, want 403", name, r.Status)
		}
	}
	var n int
	_ = f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM deeds_deed WHERE is_test AND owner_id = $1`, f.staff.ID).Scan(&n)
	if n != 0 {
		t.Errorf("staff created %d test deeds", n)
	}
	page := c.get(f.rev("core:dashboard"))
	if page.contains("Test area") || page.contains(f.rev("accounts:organization")) || page.contains("secret-a") {
		t.Errorf("the dashboard shows admin-only things to staff")
	}
}

func TestAdminSeesTestAreaAndPortalPassword(t *testing.T) {
	f := newRoleFixture(t)
	c := f.client().login(f.admin)
	if !c.get(f.rev("core:dashboard")).contains("Test area") {
		t.Errorf("admin dashboard lacks the test area")
	}
	org := c.get(f.rev("accounts:organization"))
	for _, want := range []string{"portal-a", "secret-a", f.staff.Username} {
		if !org.contains(want) {
			t.Errorf("organization page lacks %q", want)
		}
	}
	if org.contains(f.otherStaff.Username) {
		t.Errorf("organization page lists another organization's staff")
	}
}

func TestAdminChangesPortalLoginAndBlankPasswordKeepsIt(t *testing.T) {
	f := newRoleFixture(t)
	c := f.client().login(f.admin)
	url := f.rev("accounts:organization")
	data := map[string]string{"action": "portal", "portal-portal_username": "portal-new", "portal-password": "",
		"portal-new_deed_menu_index": "1", "portal-my_deeds_menu_index": "2"}
	c.post(url, data)
	check := func(user, password string) {
		t.Helper()
		cred, err := f.st.PortalCredentialFor(context.Background(), f.org.ID)
		if err != nil || cred == nil {
			t.Fatal(err)
		}
		got, err := portalcrypt.Decrypt(f.cfg.FieldEncryptionKey, cred.PortalPassword)
		if err != nil || cred.PortalUsername != user || got != password {
			t.Errorf("portal login = %q/%q (%v), want %q/%q", cred.PortalUsername, got, err, user, password)
		}
	}
	check("portal-new", "secret-a")
	data["portal-password"] = "secret-new"
	c.post(url, data)
	check("portal-new", "secret-new")
}

func TestAdminAddsAndTurnsOffStaff(t *testing.T) {
	f := newRoleFixture(t)
	c := f.client().login(f.admin)
	username := "newperson-" + f.tag
	c.post(f.rev("accounts:organization"), map[string]string{"action": "add_staff", "staff-full_name": "New Person",
		"staff-username": username, "staff-password": "a-long-enough-pass-91"})
	user, err := f.st.UserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	f.users = append(f.users, user.ID)
	m, err := f.st.MembershipOf(context.Background(), user.ID)
	if err != nil || m == nil || m.OrganizationID != f.org.ID || m.Role != models.RoleStaff {
		t.Fatalf("membership = %+v (%v)", m, err)
	}
	if user.FirstName != "New" || user.LastName != "Person" {
		t.Errorf("name = %q %q", user.FirstName, user.LastName)
	}
	c.post(f.rev("accounts:staff_toggle_active", user.ID), nil)
	user, _ = f.st.UserByUsername(context.Background(), username)
	if user.IsActive {
		t.Errorf("the staff member is still active")
	}
}

func TestAdminCannotTouchAnotherOrganizationsStaffOrThemself(t *testing.T) {
	f := newRoleFixture(t)
	c := f.client().login(f.admin)
	for _, target := range []*models.User{f.otherStaff, f.admin} {
		if r := c.post(f.rev("accounts:staff_toggle_active", target.ID), nil); r.Status != 404 {
			t.Errorf("toggling %s = %d, want 404", target.Username, r.Status)
		}
		u, _ := f.st.UserByUsername(context.Background(), target.Username)
		if !u.IsActive {
			t.Errorf("%s was deactivated", target.Username)
		}
	}
}

func TestOneAdminPerOrganization(t *testing.T) {
	f := newRoleFixture(t)
	u := &models.User{Username: "second-admin-" + f.tag, IsActive: true}
	if err := f.st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	f.users = append(f.users, u.ID)
	err := f.st.CreateMembership(context.Background(), &models.Membership{UserID: u.ID, OrganizationID: f.org.ID, Role: models.RoleAdmin})
	if err == nil || !strings.Contains(err.Error(), "23505") && !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Errorf("a second admin was accepted: %v", err)
	}
}
