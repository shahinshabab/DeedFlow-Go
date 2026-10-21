package mail

import (
	"bufio"
	"context"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	stdmail "net/mail"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMessageBytes(t *testing.T) {
	m := &Message{From: "DeedFlow <d@example.com>", To: []string{"a@example.com"}, Subject: "പെൻഡിംഗ് 2 receipts",
		Text: "Hello — ₹100\nsecond line", HTML: "<p>Hello — ₹100</p>"}
	parsed, err := stdmail.ReadMessage(strings.NewReader(string(m.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mimeDecoder)
	if s, err := dec.header(parsed.Header.Get("Subject")); err != nil || s != m.Subject {
		t.Fatalf("subject %q %v", s, err)
	}
	if !strings.HasPrefix(parsed.Header.Get("Content-Type"), "multipart/alternative") {
		t.Fatal(parsed.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(parsed.Body)
	if !strings.Contains(string(body), "text/plain") || !strings.Contains(string(body), "text/html") {
		t.Fatal(string(body))
	}
}

func TestSESSigns(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte("<SendRawEmailResponse/>"))
	}))
	defer srv.Close()
	ses := &SES{Region: "ap-south-1", Default: "d@example.com", Endpoint: srv.URL + "/",
		Now: func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }}
	if err := ses.Send(context.Background(), &Message{To: []string{"a@example.com"}, Subject: "s", Text: "t"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261021/ap-south-1/ses/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=") {
		t.Fatalf("auth = %s", gotAuth)
	}
	v, _ := url.ParseQuery(gotBody)
	if v.Get("Action") != "SendRawEmail" || v.Get("Destinations.member.1") != "a@example.com" || v.Get("RawMessage.Data") == "" {
		t.Fatalf("body = %s", gotBody)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 403) })
	if err := ses.Send(context.Background(), &Message{To: []string{"a@example.com"}, Subject: "s", Text: "t"}); err == nil {
		t.Fatal("a 403 must be an error")
	}
}

func TestSMTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		say("220 test")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case inData && line == ".":
				inData = false
				got <- data.String()
				say("250 ok")
			case inData:
				data.WriteString(line + "\n")
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				say("250 hello")
			case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
				say("250 ok")
			case line == "DATA":
				inData = true
				say("354 go")
			case line == "QUIT":
				say("221 bye")
				return
			}
		}
	}()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	s := &SMTP{Host: host, Port: port, Default: "d@example.com", Timeout: 5 * time.Second}
	if err := s.Send(context.Background(), &Message{To: []string{"A <a@example.com>"}, Subject: "hi", Text: "body"}); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-got:
		if !strings.Contains(text, "Subject: hi") || !strings.Contains(text, "body") {
			t.Fatal(text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing delivered")
	}
}

type mimeDecoder struct{}

func (mimeDecoder) header(s string) (string, error) {
	return new(mime.WordDecoder).DecodeHeader(s)
}
