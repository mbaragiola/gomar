package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const syntheticAuthority = "gomar.test"

func TestProtectedUnixAuthorityAndBoundary(t *testing.T) {
	for _, untrusted := range []string{"", "GOMAR.test", "test", "127.0.0.1", "gomar.test.", "gomar.test:443",
		"bogus@gomar.test", "https://gomar.test", "bad..test", "é.test", "bad-.test", "bad.test/path", "\r\n.test"} {
		if validUnixAuthority(untrusted) {
			t.Errorf("accepted noncanonical authority %q", untrusted)
		}
	}
	if !validUnixAuthority(syntheticAuthority) {
		t.Fatal("synthetic canonical authority refused")
	}
	i := &unixIngress{}
	wrap := i.wrapHTTPS(syntheticAuthority, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil || !isPrivateHTTPS(r) || !effectiveHTTPS(r) {
			t.Fatal("not a private effective-HTTPS context")
		}
		for _, header := range []string{"Forwarded", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-For", "X-Real-Ip"} {
			if r.Header.Get(header) != "" {
				t.Fatalf("proxy header reached application: %s", header)
			}
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		name, target string
		marked       bool
		proto        []string
		want         int
	}{
		{"valid", "/", true, []string{"https"}, 204},
		{"outside verified listener", "/", false, []string{"https"}, 400},
		{"absolute URL", "https://gomar.test/", true, []string{"https"}, 400},
		{"asterisk", "*", true, []string{"https"}, 400},
		{"http", "/", true, []string{"http"}, 400},
		{"absent", "/", true, nil, 400},
		{"different case", "/", true, []string{"HTTPS"}, 400},
		{"duplicate", "/", true, []string{"https", "https"}, 400},
		{"list", "/", true, []string{"https, http"}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.target, nil)
			r.Host = syntheticAuthority
			for _, h := range []string{"Forwarded", "X-Forwarded-Host", "X-Forwarded-For", "X-Real-Ip"} {
				r.Header.Set(h, "untrusted.fixture")
			}
			if tc.marked {
				r = r.WithContext(context.WithValue(r.Context(), privateUnixPeerKey{}, i))
			}
			r.Header["X-Forwarded-Proto"] = tc.proto
			w := httptest.NewRecorder()
			wrap.ServeHTTP(w, r)
			if w.Code != tc.want || len(w.Result().Cookies()) != 0 || w.Header().Get("Location") != "" {
				t.Fatalf("boundary status %d", w.Code)
			}
		})
	}
}

func localUnixTestDirectory(t *testing.T) string {
	t.Helper()
	base := os.Getenv("GOMAR_UNIX_TEST_PARENT")
	if base == "" {
		t.Skip("explicit short owned scratch path required for AF_UNIX integration")
	}
	if !validUnixSocketPath(filepath.Join(base, "t/socket")) {
		t.Fatal("unsafe Unix test base")
	}
	fd, _, err := trustedUnixParent(filepath.Join(base, "fixture"), uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	// Close the validation descriptor before using the strictly owned fixture path.
	_ = os.NewFile(uintptr(fd), "fixture-parent").Close()
	path, err := os.MkdirTemp(base, "u-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func TestProtectedUnixRealPathRefusals(t *testing.T) {
	dir := localUnixTestDirectory(t)
	for _, pathname := range []string{"relative.sock", "/", dir + "/../leaf", dir + "/socket\n"} {
		if l, err := listenProtectedUnix(pathname); err == nil {
			l.listener.Close()
			t.Fatalf("accepted unsafe pathname %q", pathname)
		}
	}
	file := filepath.Join(dir, "ordinary")
	if err := os.WriteFile(file, []byte("synthetic retained bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(child, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(child, alias); err != nil {
		t.Fatal(err)
	}
	for _, pathname := range []string{file, link, filepath.Join(alias, "socket"), filepath.Join(child, "socket")} {
		if l, err := listenProtectedUnix(pathname); err == nil {
			l.listener.Close()
			t.Fatalf("accepted existing or untrusted path %q", pathname)
		}
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "synthetic retained bytes" {
		t.Fatal("existing file changed")
	}
	if want, err := os.Readlink(link); err != nil || want != file {
		t.Fatal("existing symlink changed")
	}
	if err := os.Chmod(child, 0777); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(child, "private")
	if err := os.Mkdir(inner, 0700); err != nil {
		t.Fatal(err)
	}
	if l, err := listenProtectedUnix(filepath.Join(inner, "socket")); err == nil {
		l.listener.Close()
		t.Fatal("accepted writable ancestor")
	}
	path := filepath.Join(dir, "socket")
	l, err := listenProtectedUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.listener.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode() != os.ModeSocket|0600 {
		t.Fatalf("expected retained owner-only socket %v", err)
	}
	if again, err := listenProtectedUnix(path); err == nil {
		again.listener.Close()
		t.Fatal("stale socket was replaced")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("stale inode changed on refusal")
	}
}

func TestProtectedUnixRealTransportAndHealth(t *testing.T) {
	t.Run("verified UID", func(t *testing.T) { checkUnixTransportAndHealth(t, true) })
	t.Run("mismatched expected UID", func(t *testing.T) { checkUnixTransportAndHealth(t, false) })
}

func checkUnixTransportAndHealth(t *testing.T, uidMatches bool) {
	t.Helper()
	_, db := testPostgresDB(t)
	dir := localUnixTestDirectory(t)
	path := filepath.Join(dir, "socket")
	ingress, err := listenProtectedUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	if !uidMatches {
		// Keep the real account, listener and filesystem permissions fixed.
		// Only the fixture's expected UID is changed.
		ingress.uid ^= 1
	}
	application := &app{db: db, host: syntheticAuthority}
	server := &http.Server{
		Handler:     ingress.wrapHTTPS(syntheticAuthority, application.routes()),
		BaseContext: ingress.baseContext, ConnContext: ingress.connContext, DisableGeneralOptionsHandler: true,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ingress.listener) }()
	defer func() {
		_ = server.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Errorf("close: %v", err)
		}
	}()
	dialer := net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 3 * time.Second}
	check := func(host string, proto []string, want int, bodyFragment string) {
		t.Helper()
		r, err := http.NewRequest("GET", "http://"+syntheticAuthority+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Host = host
		r.Header["X-Forwarded-Proto"] = proto
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want || !strings.Contains(string(body), bodyFragment) || len(resp.Cookies()) != 0 {
			t.Fatalf("health status %d: %s %v", resp.StatusCode, body, err)
		}
	}
	if !uidMatches {
		check(syntheticAuthority, []string{"https"}, 400, "Invalid protected ingress request")
		return
	}
	check(syntheticAuthority, []string{"https"}, 200, `"status":"ok"`)
	check("foreign.test", []string{"https"}, 400, "Invalid protected ingress request")
	check(syntheticAuthority, nil, 400, "Invalid protected ingress request")
	check(syntheticAuthority, []string{"https", "http"}, 400, "Invalid protected ingress request")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	check(syntheticAuthority, []string{"https"}, 500, "No se pudo completar la operación.")
}
