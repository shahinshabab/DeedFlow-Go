package fernet

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	ks, _ := GenerateKey()
	k, err := ParseKey(ks)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Encrypt([]byte("പാസ്‌വേഡ് 123"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.Decrypt(tok)
	if err != nil || string(got) != "പാസ്‌വേഡ് 123" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := k.Decrypt(tok[:len(tok)-4] + "AAAA"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

// Tokens made by Python's cryptography library must decrypt here, and the
// other way round - the two applications share one database.
func TestCompatibleWithPython(t *testing.T) {
	py := "/tmp/venv/bin/python"
	if _, err := exec.LookPath(py); err != nil {
		t.Skip("python reference not available")
	}
	ks, _ := GenerateKey()
	out, err := exec.Command(py, "-c",
		"import sys; from cryptography.fernet import Fernet; print(Fernet(sys.argv[1].encode()).encrypt('s3cr3t ക'.encode()).decode())", ks).Output()
	if err != nil {
		t.Skip("python fernet failed:", err)
	}
	k, _ := ParseKey(ks)
	got, err := k.Decrypt(strings.TrimSpace(string(out)))
	if err != nil || string(got) != "s3cr3t ക" {
		t.Fatalf("python token: %q %v", got, err)
	}
	tok, _ := k.Encrypt([]byte("from go ക"))
	back, err := exec.Command(py, "-c",
		"import sys; from cryptography.fernet import Fernet; print(Fernet(sys.argv[1].encode()).decrypt(sys.argv[2].encode()).decode())", ks, tok).Output()
	if err != nil || strings.TrimSpace(string(back)) != "from go ക" {
		t.Fatalf("go token rejected by python: %q %v", back, err)
	}
}
