package mail

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type formValues [][2]string

func (f formValues) encode() string {
	sorted := append(formValues(nil), f...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i][0] < sorted[j][0] })
	parts := make([]string, len(sorted))
	for i, kv := range sorted {
		parts[i] = url.QueryEscape(kv[0]) + "=" + url.QueryEscape(kv[1])
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// post sends an AWS Query API request signed with Signature Version 4.
func (s *SES) post(ctx context.Context, body, access, secret, session string) error {
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = "https://email." + s.Region + ".amazonaws.com/"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	t := now().UTC()
	amzDate, day := t.Format("20060102T150405Z"), t.Format("20060102")
	const service = "ses"
	scope := day + "/" + s.Region + "/" + service + "/aws4_request"

	headers := map[string]string{
		"content-type": "application/x-www-form-urlencoded; charset=utf-8",
		"host":         u.Host,
		"x-amz-date":   amzDate,
	}
	if session != "" {
		headers["x-amz-security-token"] = session
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{"POST", path, "", canonHeaders.String(), signed, sha256Hex(body)}, "\n")
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex(canonical)}, "\n")
	key := hmacSHA256([]byte("AWS4"+secret), day)
	key = hmacSHA256(key, s.Region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range headers {
		if k != "host" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		access, scope, signed, signature))
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("SES answered %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
