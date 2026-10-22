package web

import (
	"net/http/httptest"
	"testing"

	"github.com/shahinshabab/deedflow-go/internal/config"
)

// A Codespace's forwarded port reaches the app as localhost; the browser's
// Origin is the public address, which SITE_URL names.
func TestTrustedHostSiteURL(t *testing.T) {
	a := &App{Cfg: &config.Config{SiteURL: "https://demo-8001.app.github.dev", AllowedHosts: []string{"localhost", ".app.github.dev"}}}
	c := &Ctx{App: a, R: httptest.NewRequest("POST", "http://localhost:8001/login/", nil)}
	for host, want := range map[string]bool{
		"demo-8001.app.github.dev":  true,
		"localhost:8001":            true,
		"other-8001.app.github.dev": false,
		"evil.example":              false,
	} {
		if got := c.trustedHost(host); got != want {
			t.Errorf("trustedHost(%q) = %v, want %v", host, got, want)
		}
	}
}
