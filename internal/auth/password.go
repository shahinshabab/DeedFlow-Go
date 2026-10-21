// Package auth holds password hashing compatible with Django's formats (so
// users created by the Django project can sign in), password validation and
// the session machinery.
package auth

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/pbkdf2"

	"github.com/shahinshabab/deedflow-go/internal/fuzzy"
)

// Iterations is Django 5.1's PBKDF2 work factor.
const Iterations = 870000

const saltChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomSalt() string {
	b := make([]byte, 22)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = saltChars[int(b[i])%len(saltChars)]
	}
	return string(b)
}

// MakePassword hashes a password as Django's PBKDF2PasswordHasher does.
func MakePassword(password string) string {
	return makePBKDF2(password, randomSalt(), Iterations)
}

func makePBKDF2(password, salt string, iterations int) string {
	h := pbkdf2.Key([]byte(password), []byte(salt), iterations, sha256.Size, sha256.New)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iterations, salt, base64.StdEncoding.EncodeToString(h))
}

// UnusablePassword marks an account that can't sign in with a password.
func UnusablePassword() string { return "!" + randomSalt() + randomSalt()[:18] }

// CheckPassword verifies a password against a Django-encoded hash.
func CheckPassword(password, encoded string) bool {
	if encoded == "" || strings.HasPrefix(encoded, "!") {
		return false
	}
	parts := strings.Split(encoded, "$")
	switch {
	case len(parts) == 4 && (parts[0] == "pbkdf2_sha256" || parts[0] == "pbkdf2_sha1"):
		iterations, err := strconv.Atoi(parts[1])
		if err != nil || iterations < 1 {
			return false
		}
		want, err := base64.StdEncoding.DecodeString(parts[3])
		if err != nil {
			return false
		}
		hashFn := sha256.New
		if parts[0] == "pbkdf2_sha1" {
			hashFn = sha1.New
		}
		got := pbkdf2.Key([]byte(password), []byte(parts[2]), iterations, len(want), hashFn)
		return subtle.ConstantTimeCompare(got, want) == 1
	case strings.HasPrefix(encoded, "bcrypt_sha256$"):
		// bcrypt_sha256$$2b$12$... : the password is hmac-sha256'd, hex encoded, first.
		digest := hmac.New(sha256.New, nil)
		digest.Write([]byte(password))
		pre := hex.EncodeToString(digest.Sum(nil))
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(encoded, "bcrypt_sha256$")), []byte(pre)) == nil
	case strings.HasPrefix(encoded, "bcrypt$"):
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(encoded, "bcrypt$")), []byte(password)) == nil
	}
	return false
}

// NeedsRehash reports whether the hash uses weaker settings than MakePassword.
func NeedsRehash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) == 4 && parts[0] == "pbkdf2_sha256" {
		n, _ := strconv.Atoi(parts[1])
		return n < Iterations
	}
	return true
}

// --- validation ----------------------------------------------------------

//go:embed common-passwords.txt.gz
var commonGz []byte

var (
	commonOnce sync.Once
	common     map[string]struct{}
)

func commonPasswords() map[string]struct{} {
	commonOnce.Do(func() {
		common = map[string]struct{}{}
		zr, err := gzip.NewReader(bytes.NewReader(commonGz))
		if err != nil {
			return
		}
		body, _ := io.ReadAll(zr)
		for _, line := range strings.Split(string(body), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				common[line] = struct{}{}
			}
		}
	})
	return common
}

// UserAttrs are the user fields the similarity validator compares against.
type UserAttrs struct{ Username, FirstName, LastName, Email string }

func splitWords(s string) []string {
	var parts []string
	cur := strings.Builder{}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur.WriteRune(r)
		} else {
			parts = append(parts, cur.String())
			cur.Reset()
		}
	}
	parts = append(parts, cur.String())
	return parts
}

// ValidatePassword applies Django's four default validators
// (AUTH_PASSWORD_VALIDATORS) and returns every message.
func ValidatePassword(password string, user *UserAttrs) []string {
	var errs []string
	if user != nil {
		pw := strings.ToLower(password)
		for _, value := range []string{user.Username, user.FirstName, user.LastName, user.Email} {
			if value == "" {
				continue
			}
			lower := strings.ToLower(value)
			similar := false
			for _, part := range append(splitWords(lower), lower) {
				pwLen, partLen := len([]rune(pw)), len([]rune(part))
				if pwLen >= 10*partLen && float64(partLen) < 0.7/2*float64(pwLen) {
					continue
				}
				if fuzzy.NewSequenceMatcher(pw, part).QuickRatio() >= 0.7 {
					similar = true
					break
				}
			}
			if similar {
				errs = append(errs, "The password is too similar to the "+attrName(user, value)+".")
				break
			}
		}
	}
	if len([]rune(password)) < 8 {
		errs = append(errs, "This password is too short. It must contain at least 8 characters.")
	}
	if _, bad := commonPasswords()[strings.ToLower(strings.TrimSpace(password))]; bad {
		errs = append(errs, "This password is too common.")
	}
	if password != "" && strings.Trim(password, "0123456789") == "" {
		errs = append(errs, "This password is entirely numeric.")
	}
	return errs
}

func attrName(u *UserAttrs, value string) string {
	switch value {
	case u.Username:
		return "username"
	case u.FirstName:
		return "first name"
	case u.LastName:
		return "last name"
	}
	return "email address"
}
