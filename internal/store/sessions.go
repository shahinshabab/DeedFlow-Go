package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// SessionRow is a stored browser session.
type SessionRow struct {
	Key       string
	UserID    *int64
	Data      map[string]any
	ExpiresAt time.Time
}

// NewSessionKey makes an unguessable session key.
func NewSessionKey() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// LoadSession returns a live session, or nil when the key is unknown or expired.
func (s *Store) LoadSession(ctx context.Context, key string) (*SessionRow, error) {
	var (
		row  SessionRow
		data []byte
	)
	err := s.q().QueryRow(ctx, `SELECT key, user_id, data, expires_at FROM df_session WHERE key = $1 AND expires_at > now()`, key).
		Scan(&row.Key, &row.UserID, &data, &row.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Data = map[string]any{}
	_ = json.Unmarshal(data, &row.Data)
	return &row, nil
}

// SaveSession inserts or updates a session.
func (s *Store) SaveSession(ctx context.Context, row *SessionRow) error {
	data, err := json.Marshal(row.Data)
	if err != nil {
		return err
	}
	_, err = s.q().Exec(ctx, `INSERT INTO df_session (key, user_id, data, expires_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (key) DO UPDATE SET user_id = EXCLUDED.user_id, data = EXCLUDED.data, expires_at = EXCLUDED.expires_at`,
		row.Key, row.UserID, data, row.ExpiresAt)
	return err
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, key string) error {
	_, err := s.q().Exec(ctx, `DELETE FROM df_session WHERE key = $1`, key)
	return err
}

// PurgeSessions drops expired sessions.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.q().Exec(ctx, `DELETE FROM df_session WHERE expires_at < now()`)
	return err
}
