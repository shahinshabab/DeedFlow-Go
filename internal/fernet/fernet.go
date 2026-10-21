// Package fernet implements Fernet symmetric encryption (AES-128-CBC with an
// HMAC-SHA256 tag), byte-compatible with Python's cryptography.fernet, so
// portal passwords encrypted by the Django project still decrypt.
package fernet

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

// ErrInvalid is returned for a token that fails verification or decoding.
var ErrInvalid = errors.New("fernet: invalid token")

// Key is a decoded Fernet key: 16 bytes of signing key, 16 of encryption key.
type Key struct {
	signing    [16]byte
	encryption [16]byte
}

// ParseKey decodes a url-safe base64 Fernet key (as printed by Fernet.generate_key()).
func ParseKey(s string) (*Key, error) {
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return nil, errors.New("fernet: key is not valid url-safe base64")
		}
	}
	if len(raw) != 32 {
		return nil, errors.New("fernet: key must be 32 url-safe base64-encoded bytes")
	}
	k := &Key{}
	copy(k.signing[:], raw[:16])
	copy(k.encryption[:], raw[16:])
	return k, nil
}

// GenerateKey makes a new random key string.
func GenerateKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

// Encrypt returns the Fernet token for plaintext.
func (k *Key) Encrypt(plaintext []byte) (string, error) {
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	return k.encryptAt(plaintext, time.Now(), iv)
}

func (k *Key) encryptAt(plaintext []byte, now time.Time, iv []byte) (string, error) {
	block, err := aes.NewCipher(k.encryption[:])
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append(append([]byte{}, plaintext...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

	msg := make([]byte, 0, 1+8+16+len(ct)+32)
	msg = append(msg, 0x80)
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(now.Unix()))
	msg = append(msg, ts[:]...)
	msg = append(msg, iv...)
	msg = append(msg, ct...)
	mac := hmac.New(sha256.New, k.signing[:])
	mac.Write(msg)
	msg = mac.Sum(msg)
	return base64.URLEncoding.EncodeToString(msg), nil
}

// Decrypt verifies and decrypts a token. There is no TTL check, like
// Fernet.decrypt() without a ttl.
func (k *Key) Decrypt(token string) ([]byte, error) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(raw) < 1+8+16+16+32 || raw[0] != 0x80 {
		return nil, ErrInvalid
	}
	body, tag := raw[:len(raw)-32], raw[len(raw)-32:]
	mac := hmac.New(sha256.New, k.signing[:])
	mac.Write(body)
	if subtle.ConstantTimeCompare(mac.Sum(nil), tag) != 1 {
		return nil, ErrInvalid
	}
	iv, ct := body[9:25], body[25:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, ErrInvalid
	}
	block, err := aes.NewCipher(k.encryption[:])
	if err != nil {
		return nil, err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pad := int(pt[len(pt)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(pt) {
		return nil, ErrInvalid
	}
	for _, b := range pt[len(pt)-pad:] {
		if int(b) != pad {
			return nil, ErrInvalid
		}
	}
	return pt[:len(pt)-pad], nil
}
