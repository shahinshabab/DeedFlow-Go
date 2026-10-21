package auth

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDjangoHashesVerify(t *testing.T) {
	py := "/tmp/venv/bin/python"
	if _, err := exec.LookPath(py); err != nil {
		t.Skip("python reference not available")
	}
	script := `
import sys, django
from django.conf import settings
settings.configure()
from django.contrib.auth.hashers import make_password
print(make_password(sys.argv[1]))`
	out, err := exec.Command(py, "-c", script, "pässword ക 123").Output()
	if err != nil {
		t.Skip("django not importable:", err)
	}
	encoded := strings.TrimSpace(string(out))
	if !CheckPassword("pässword ക 123", encoded) {
		t.Fatalf("django hash %q rejected", encoded)
	}
	if CheckPassword("wrong", encoded) {
		t.Fatal("wrong password accepted")
	}
	mine := MakePassword("hunter22 trouble")
	chk := `
import sys
from django.conf import settings
settings.configure()
from django.contrib.auth.hashers import check_password
print(check_password(sys.argv[1], sys.argv[2]))`
	res, err := exec.Command(py, "-c", chk, "hunter22 trouble", mine).Output()
	if err != nil || strings.TrimSpace(string(res)) != "True" {
		t.Fatalf("django rejects our hash %q: %q %v", mine, res, err)
	}
}

func TestValidators(t *testing.T) {
	cases := map[string]bool{
		"short":          false,
		"12345678":       false,
		"password":       false,
		"qwertyuiop":     false,
		"correct-horse7": true,
	}
	for pw, ok := range cases {
		if got := len(ValidatePassword(pw, &UserAttrs{Username: "mohammedali"})) == 0; got != ok {
			t.Errorf("%q valid=%v want %v (%v)", pw, got, ok, ValidatePassword(pw, nil))
		}
	}
	if errs := ValidatePassword("mohammedali1", &UserAttrs{Username: "mohammedali"}); len(errs) == 0 {
		t.Error("password similar to username accepted")
	}
}
