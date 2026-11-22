package mcpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const fakeToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestOriginAndTokenValidation(t *testing.T) {
	for _, origin := range []string{"http://localhost", "http://192.0.2.1", "https://secret@example.invalid", "https://example.invalid/token", "https://example.invalid/?secret=1", "file://example.invalid", "https://example.invalid/#secret", "http://[::1%25lo]"} {
		if _, err := New(origin, fakeToken); err == nil {
			t.Errorf("accepted disallowed origin %q", origin)
		}
	}
	for _, origin := range []string{"https://gomar.example.invalid", "http://127.0.0.1:4242", "http://[::1]:4444"} {
		if _, err := New(origin, fakeToken); err != nil {
			t.Errorf("valid origin %q: %v", origin, err)
		}
	}
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte(fakeToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if token, err := ReadToken(path); token != fakeToken || err != nil {
		t.Fatal("failed to read private credential")
	}
	symlink := path + "-link"
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken(symlink); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken(path); err == nil {
		t.Fatal("accepted group-readable credential")
	}
	if _, err := New("https://gomar.example.invalid", fakeToken+"\n"); err == nil {
		t.Fatal("accepted malformed token")
	}
}

func TestClientDeniesRedirectsMalformedAndOversizedResponses(t *testing.T) {
	var leaked atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { leaked.Add(1) }))
	defer dest.Close()
	for _, test := range []struct {
		name, body, format string
		status             int
	}{
		{"redirect", "", "", 301},
		{"expired", fakeToken, "application/json", 401},
		{"forbidden", fakeToken, "application/json", 403},
		{"missing", "", "application/json", 404},
		{"invalid", "null", "application/json", 200},
		{"partial", "{}", "application/json", 200},
		{"too large", strings.Repeat(" ", maxResponse+1), "application/json", 200},
		{"HTML", `{"id":1,"username":"misleading"}`, "text/html", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/mcp/me" || r.Header.Get("Authorization") != "Bearer "+fakeToken || r.Header.Get("Cookie") != "" {
					t.Error("unexpected request")
				}
				w.Header().Set("Content-Type", test.format)
				if test.status == 301 {
					w.Header().Set("Location", dest.URL+"/copied-secret")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer source.Close()
			c, err := New(source.URL, fakeToken)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Me(context.Background()); err == nil || strings.Contains(err.Error(), fakeToken) || strings.Contains(err.Error(), dest.URL) {
				t.Fatalf("untrusted response accepted or repeated: %v", err)
			}
		})
	}
	if leaked.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestClientSalesUsesExplicitScopeAndRejectsBadSummary(t *testing.T) {
	const response = `{"period":"this_month","timezone":"America/Argentina/Buenos_Aires","from":"2026-11-01T00:00:00-03:00","to":"2026-12-01T00:00:00-03:00","currency":"ARS","sale_count":1,"total_minor":2200,"outlets":[{"id":8,"name":"Centro","sale_count":1,"total_minor":2200}]}`
	var invalid atomic.Bool
	var called atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		if r.URL.Path != "/api/v1/mcp/entities/3/sales-summary" || r.URL.RawQuery != "outlet_id=8&period=this_month" {
			t.Errorf("request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		body := response
		if invalid.Load() {
			body = strings.Replace(body, `"total_minor":2200`, `"total_minor":9999`, 1)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer source.Close()
	c, err := New(source.URL, fakeToken)
	if err != nil {
		t.Fatal(err)
	}
	target := int64(8)
	if _, err = c.Sales(t.Context(), 0, &target, "this_month"); err == nil {
		t.Fatal("accepted arbitrary entity")
	}
	if _, err = c.Sales(t.Context(), 3, &target, "yesterday"); err == nil {
		t.Fatal("accepted unsupported period")
	}
	if called.Load() != 0 {
		t.Fatal("made invalid request")
	}
	got, err := c.Sales(t.Context(), 3, &target, "this_month")
	if err != nil || got.TotalMinor != 2200 || got.SaleCount != 1 || len(got.Outlets) != 1 || got.Outlets[0].Name != "Centro" {
		t.Fatal(got, err)
	}
	invalid.Store(true)
	if _, err = c.Sales(t.Context(), 3, &target, "this_month"); err == nil {
		t.Fatal("accepted mismatching totals")
	}
}
