package store

import (
	"context"
	"strconv"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

var userCols = db.Columns[models.User]("")

// UserByID loads a user.
func (s *Store) UserByID(ctx context.Context, id int64) (*models.User, error) {
	return db.One[models.User](ctx, s.q(), `SELECT `+userCols+` FROM auth_user WHERE id = $1`, id)
}

// UserByUsername finds a user by exact username (Django's is case-sensitive).
func (s *Store) UserByUsername(ctx context.Context, username string) (*models.User, error) {
	return db.One[models.User](ctx, s.q(), `SELECT `+userCols+` FROM auth_user WHERE username = $1`, username)
}

// UsernameTaken reports whether any username equals name, ignoring case.
func (s *Store) UsernameTaken(ctx context.Context, name string) (bool, error) {
	var taken bool
	err := s.q().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_user WHERE lower(username) = lower($1))`, name).Scan(&taken)
	return taken, err
}

// CreateUser inserts a user (Password must already be hashed).
func (s *Store) CreateUser(ctx context.Context, u *models.User) error {
	if u.DateJoined.IsZero() {
		u.DateJoined = now()
	}
	_, err := db.Insert(ctx, s.q(), "auth_user", u)
	return err
}

// SaveUser writes a user's fields back.
func (s *Store) SaveUser(ctx context.Context, u *models.User, cols ...string) error {
	return db.Update(ctx, s.q(), "auth_user", u, cols...)
}

// TouchLogin records the time of the last login.
func (s *Store) TouchLogin(ctx context.Context, id int64) error {
	_, err := s.q().Exec(ctx, `UPDATE auth_user SET last_login = $2 WHERE id = $1`, id, now())
	return err
}

// ListUsers returns users for the admin, newest first, optionally filtered.
func (s *Store) ListUsers(ctx context.Context, search string, limit, offset int) ([]models.User, int, error) {
	where, args := "", []any{}
	if search = strings.TrimSpace(search); search != "" {
		where = ` WHERE username ILIKE $1 OR first_name ILIKE $1 OR last_name ILIKE $1 OR email ILIKE $1`
		args = append(args, "%"+search+"%")
	}
	var total int
	if err := s.q().QueryRow(ctx, `SELECT count(*) FROM auth_user`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	users, err := db.All[models.User](ctx, s.q(), `SELECT `+userCols+` FROM auth_user`+where+
		` ORDER BY username LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	return users, total, err
}

// DeleteUser removes a user (cascading their memberships and deeds).
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.q().Exec(ctx, `DELETE FROM auth_user WHERE id = $1`, id)
	return err
}
