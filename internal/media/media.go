// Package media stores user-visible files (uploaded PDFs, portal
// screenshots, generated exports) under MEDIA_ROOT, the way Django's
// FileSystemStorage does: a taken name gets a short random suffix.
package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Path resolves a stored media name to a file path, refusing anything outside root.
func Path(root, name string) (string, error) {
	clean := filepath.Clean("/" + name)
	full := filepath.Join(root, clean)
	if !strings.HasPrefix(full, filepath.Clean(root)+string(filepath.Separator)) {
		return "", errors.New("path outside media root")
	}
	return full, nil
}

func randomSuffix(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// Save writes data to root/name (creating directories), picking a fresh name
// if one exists, and returns the stored name.
func Save(root, name string, data []byte) (string, error) {
	dir, base := path.Split(name)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	candidate := name
	for i := 0; ; i++ {
		full, err := Path(root, candidate)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, werr := f.Write(data)
			cerr := f.Close()
			if werr != nil {
				return "", werr
			}
			return candidate, cerr
		}
		if !os.IsExist(err) {
			return "", err
		}
		candidate = fmt.Sprintf("%s%s_%s%s", dir, stem, randomSuffix(7), ext)
		if i > 20 {
			return "", errors.New("could not find a free file name")
		}
	}
}
