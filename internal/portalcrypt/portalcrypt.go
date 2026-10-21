// Package portalcrypt encrypts the Registration Department portal password at
// rest with a dedicated key (FIELD_ENCRYPTION_KEY), kept separate from the
// session secret so rotating one doesn't affect the other.
package portalcrypt

import (
	"errors"

	"github.com/shahinshabab/deedflow-go/internal/fernet"
)

// ErrNoKey is returned when FIELD_ENCRYPTION_KEY isn't configured.
var ErrNoKey = errors.New("FIELD_ENCRYPTION_KEY is not configured - portal passwords can't be encrypted/decrypted without it; " +
	"generate one with: deedflow genkey")

func key(k string) (*fernet.Key, error) {
	if k == "" {
		return nil, ErrNoKey
	}
	return fernet.ParseKey(k)
}

// Encrypt returns the ciphertext for plaintext ("" stays "").
func Encrypt(keyStr, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	k, err := key(keyStr)
	if err != nil {
		return "", err
	}
	return k.Encrypt([]byte(plaintext))
}

// Decrypt returns the plaintext; "" for empty or undecryptable ciphertext.
func Decrypt(keyStr, ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	k, err := key(keyStr)
	if err != nil {
		return "", err
	}
	pt, err := k.Decrypt(ciphertext)
	if err != nil {
		return "", nil
	}
	return string(pt), nil
}
