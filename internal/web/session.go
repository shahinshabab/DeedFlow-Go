package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

const (
	sessionCookie = "sessionid"
	csrfCookie    = "csrftoken"
	sessionAge    = 14 * 24 * time.Hour
	csrfAge       = 365 * 24 * time.Hour
)

// Message is a one-shot notice shown on the next page (django.contrib.messages).
type Message struct {
	Tags string `json:"tags"`
	Text string `json:"text"`
}

// Session is the visitor's server-side session.
type Session struct {
	row   *store.SessionRow
	dirty bool
}

func newSession() *Session {
	return &Session{
		row:   &store.SessionRow{Key: store.NewSessionKey(), Data: map[string]any{}, ExpiresAt: time.Now().Add(sessionAge)},
	}
}

// Get reads a value.
func (s *Session) Get(key string) (any, bool) {
	v, ok := s.row.Data[key]
	return v, ok
}

// GetString reads a string value.
func (s *Session) GetString(key string) string {
	v, _ := s.row.Data[key].(string)
	return v
}

// Set stores a value.
func (s *Session) Set(key string, v any) {
	s.row.Data[key] = v
	s.dirty = true
}

// Delete removes a value.
func (s *Session) Delete(key string) {
	if _, ok := s.row.Data[key]; ok {
		delete(s.row.Data, key)
		s.dirty = true
	}
}

// addMessage queues a message.
func (s *Session) addMessage(tags, text string) {
	var list []any
	if cur, ok := s.row.Data["messages"].([]any); ok {
		list = cur
	}
	list = append(list, map[string]any{"tags": tags, "text": text})
	s.Set("messages", list)
}

// popMessages returns and clears the queued messages.
func (s *Session) popMessages() []Message {
	cur, ok := s.row.Data["messages"].([]any)
	if !ok || len(cur) == 0 {
		return nil
	}
	var out []Message
	for _, m := range cur {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, Message{Tags: strFrom(mm["tags"]), Text: strFrom(mm["text"])})
		}
	}
	s.Delete("messages")
	return out
}

func strFrom(v any) string { s, _ := v.(string); return s }

// authHash ties a session to the password it was created with (Django's
// session auth hash), so changing a password signs out the other sessions.
func authHash(secret string, u *models.User) string {
	mac := hmac.New(sha256.New, []byte(secret+"deedflow.session.auth"))
	mac.Write([]byte(u.Password))
	return hex.EncodeToString(mac.Sum(nil))
}

// --- CSRF -------------------------------------------------------------------
//
// Like Django: a random secret lives in the csrftoken cookie, and forms carry
// a masked copy of it (a random pad plus the secret XOR the pad), so the token
// in a page differs on every render.

const csrfSecretLen = 32
const csrfChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func newCSRFSecret() string {
	b := make([]byte, csrfSecretLen)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = csrfChars[int(b[i])%len(csrfChars)]
	}
	return string(b)
}

func maskCSRF(secret string) string {
	pad := newCSRFSecret()
	out := make([]byte, 0, 2*csrfSecretLen)
	out = append(out, pad...)
	for i := 0; i < csrfSecretLen; i++ {
		a := strings.IndexByte(csrfChars, secret[i])
		b := strings.IndexByte(csrfChars, pad[i])
		out = append(out, csrfChars[(a+b)%len(csrfChars)])
	}
	return string(out)
}

func unmaskCSRF(token string) string {
	if len(token) != 2*csrfSecretLen {
		return token
	}
	pad, masked := token[:csrfSecretLen], token[csrfSecretLen:]
	out := make([]byte, csrfSecretLen)
	for i := 0; i < csrfSecretLen; i++ {
		a := strings.IndexByte(csrfChars, masked[i])
		b := strings.IndexByte(csrfChars, pad[i])
		if a < 0 || b < 0 {
			return ""
		}
		out[i] = csrfChars[(a-b+len(csrfChars))%len(csrfChars)]
	}
	return string(out)
}

func validCSRFSecret(s string) bool {
	if len(s) != csrfSecretLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(csrfChars, s[i]) < 0 {
			return false
		}
	}
	return true
}

func csrfEqual(secret, token string) bool {
	if !validCSRFSecret(secret) {
		return false
	}
	switch len(token) {
	case 2 * csrfSecretLen:
		token = unmaskCSRF(token)
	case csrfSecretLen:
	default:
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(token)) == 1
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions || m == http.MethodTrace
}
