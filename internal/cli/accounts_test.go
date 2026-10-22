package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/auth"
	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Ports of SetupOrganizationCommandTests (accounts/tests.py).

func testEnv(t *testing.T) (*Env, string) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://deedflow:deedflow@127.0.0.1:5433/deedflow"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d, err := db.Connect(ctx, url)
	if err != nil {
		t.Skip("no test database: ", err)
	}
	t.Cleanup(d.Close)
	return &Env{DB: d, Store: store.New(d)}, store.NewUUID()[:8]
}

// withStdin runs fn with os.Stdin reading text (the command prompts for a password).
func withStdin(t *testing.T, text string, fn func()) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old; f.Close() }()
	fn()
}

func cleanupOrg(t *testing.T, e *Env, orgName string, usernames ...string) {
	t.Cleanup(func() {
		ctx := context.Background()
		q := func(sql string, args ...any) { _, _ = e.DB.Pool.Exec(ctx, sql, args...) }
		q(`DELETE FROM deeds_deed WHERE owner_id IN (SELECT id FROM auth_user WHERE username = ANY($1))`, usernames)
		q(`DELETE FROM accounts_membership WHERE user_id IN (SELECT id FROM auth_user WHERE username = ANY($1))`, usernames)
		q(`DELETE FROM accounts_organization WHERE name = $1`, orgName)
		q(`DELETE FROM auth_user WHERE username = ANY($1)`, usernames)
	})
}

func TestSetupOrganizationCreatesNewOrganizationAndAdmin(t *testing.T) {
	e, tag := testEnv(t)
	org, admin := "Office C "+tag, "adminc-"+tag
	cleanupOrg(t, e, org, admin)
	ctx := context.Background()
	var err error
	withStdin(t, "shortpw\n", func() {
		err = setupOrganization(ctx, e, []string{org, "--admin", admin, "--full-name", "Admin C Person"})
	})
	if err != nil {
		t.Fatal(err)
	}
	user, err := e.Store.UserByUsername(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword("shortpw", user.Password) {
		t.Errorf("the admin's password was not set")
	}
	if user.FirstName != "Admin" || user.LastName != "C Person" {
		t.Errorf("name = %q %q", user.FirstName, user.LastName)
	}
	m, err := e.Store.MembershipOf(ctx, user.ID)
	if err != nil || m == nil || !m.IsAdmin() {
		t.Fatalf("membership = %+v (%v)", m, err)
	}
	o, _ := e.Store.OrganizationByID(ctx, m.OrganizationID)
	if o.Name != org {
		t.Errorf("organization = %q", o.Name)
	}
}

func TestSetupOrganizationRenamesAnExistingAdminsOrganizationKeepingDeeds(t *testing.T) {
	e, tag := testEnv(t)
	oldName, newName, admin := "old "+tag, "Renamed "+tag, "owner-"+tag
	cleanupOrg(t, e, oldName, admin)
	cleanupOrg(t, e, newName)
	ctx := context.Background()
	org := &models.Organization{Name: oldName}
	if err := e.Store.CreateOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	user := &models.User{Username: admin, IsActive: true, Password: auth.MakePassword("pw-for-tests-123")}
	if err := e.Store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.CreateMembership(ctx, &models.Membership{UserID: user.ID, OrganizationID: org.ID, Role: models.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.CreateDeed(ctx, &models.Deed{OwnerID: user.ID}); err != nil {
		t.Fatal(err)
	}
	// No password is asked for an existing admin: an empty stdin would fail the prompt.
	var err error
	withStdin(t, "", func() { err = setupOrganization(ctx, e, []string{newName, "--admin", admin}) })
	if err != nil {
		t.Fatal(err)
	}
	renamed, _ := e.Store.OrganizationByID(ctx, org.ID)
	if renamed.Name != newName {
		t.Errorf("name = %q", renamed.Name)
	}
	if n, _ := e.Store.CountDeedsOf(ctx, org.ID); n != 1 {
		t.Errorf("%d deeds kept, want 1", n)
	}
	after, _ := e.Store.UserByUsername(ctx, admin)
	if !auth.CheckPassword("pw-for-tests-123", after.Password) {
		t.Errorf("the admin's password changed")
	}
	if strings.TrimSpace(after.FirstName) != "" {
		t.Errorf("first name = %q", after.FirstName)
	}
}
