package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/auth"
	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

func init() {
	commands["createsuperuser"] = createSuperuser
	commands["setup_organization"] = setupOrganization
}

func createSuperuser(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("createsuperuser", flag.ContinueOnError)
	username := fs.String("username", "", "username")
	email := fs.String("email", "", "email address")
	passwordFlag := fs.String("password", "", "password (prompted if omitted; avoid on shared machines)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		u, err := prompt("Username: ", false)
		if err != nil {
			return err
		}
		*username = strings.TrimSpace(u)
	}
	if *username == "" {
		return errors.New("a username is required")
	}
	if taken, err := e.Store.UsernameTaken(ctx, *username); err != nil {
		return err
	} else if taken {
		return fmt.Errorf("the username %q is already taken", *username)
	}
	pw := *passwordFlag
	if pw == "" {
		p1, err := prompt("Password: ", true)
		if err != nil {
			return err
		}
		p2, err := prompt("Password (again): ", true)
		if err != nil {
			return err
		}
		if p1 != p2 {
			return errors.New("passwords didn't match")
		}
		if msgs := auth.ValidatePassword(p1, &auth.UserAttrs{Username: *username, Email: *email}); len(msgs) > 0 {
			return errors.New(strings.Join(msgs, " "))
		}
		pw = p1
	}
	if pw == "" {
		return errors.New("a password is required")
	}
	u := &models.User{Username: *username, Email: *email, Password: auth.MakePassword(pw), IsStaff: true, IsSuperuser: true, IsActive: true}
	if err := e.Store.CreateUser(ctx, u); err != nil {
		return err
	}
	fmt.Println("Superuser created successfully.")
	return nil
}

// setupOrganization creates an organization with its admin, or renames an
// existing admin's organization (keeping its deeds and portal login).
func setupOrganization(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("setup_organization", flag.ContinueOnError)
	admin := fs.String("admin", "", "username of the organization's admin")
	fullName := fs.String("full-name", "", "the admin's full name")
	setPassword := fs.Bool("set-password", false, "prompt for the admin's password")
	// Go's flag package stops at the first positional; accept the name anywhere.
	var name string
	var rest []string
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") && name == "" {
			name = args[i]
			continue
		}
		rest = append(rest, args[i])
		if (args[i] == "--admin" || args[i] == "-admin" || args[i] == "--full-name" || args[i] == "-full-name") && i+1 < len(args) {
			i++
			rest = append(rest, args[i])
		}
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" || *admin == "" {
		return errors.New(`usage: deedflow setup_organization "Organization name" --admin USERNAME [--full-name "Name"] [--set-password]`)
	}
	user, err := e.Store.UserByUsername(ctx, *admin)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	newPassword := ""
	if user == nil || *setPassword {
		newPassword, err = prompt(fmt.Sprintf("DeedFlow password for %s: ", *admin), true)
		if err != nil {
			return err
		}
		if newPassword == "" {
			return errors.New("a password is required")
		}
	}
	return e.Store.InTx(ctx, func(tx *storeT) error {
		if user == nil {
			user = &models.User{Username: *admin, IsActive: true}
			user.Password = auth.UnusablePassword()
			if err := tx.CreateUser(ctx, user); err != nil {
				return err
			}
			fmt.Printf("Created user %s.\n", *admin)
		}
		membership, err := tx.MembershipOf(ctx, user.ID)
		if err != nil {
			return err
		}
		if membership != nil && !membership.IsAdmin() {
			org, _ := tx.OrganizationByID(ctx, membership.OrganizationID)
			return fmt.Errorf("%s is staff in %s; not changing that", *admin, org.Name)
		}
		if membership != nil {
			org, err := tx.OrganizationByID(ctx, membership.OrganizationID)
			if err != nil {
				return err
			}
			old := org.Name
			org.Name = name
			if err := tx.SaveOrganization(ctx, org); err != nil {
				return err
			}
			n, _ := tx.CountDeedsOf(ctx, org.ID)
			fmt.Printf("Renamed organization %q to %q (%d deeds kept).\n", old, name, n)
		} else {
			org := &models.Organization{Name: name}
			if err := tx.CreateOrganization(ctx, org); err != nil {
				return err
			}
			if err := tx.CreateMembership(ctx, &models.Membership{UserID: user.ID, OrganizationID: org.ID, Role: models.RoleAdmin}); err != nil {
				return err
			}
			fmt.Printf("Created organization %q with admin %s.\n", name, *admin)
		}
		if *fullName != "" {
			user.FirstName, user.LastName, _ = strings.Cut(strings.TrimSpace(*fullName), " ")
		}
		if newPassword != "" {
			user.Password = auth.MakePassword(newPassword)
		}
		if err := tx.SaveUser(ctx, user); err != nil {
			return err
		}
		fmt.Printf("%s is the admin of %q.\n", *admin, name)
		return nil
	})
}
