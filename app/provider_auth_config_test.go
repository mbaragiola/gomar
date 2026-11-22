package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func providerAppleSecretFixture(client string, now time.Time, modify func(map[string]any, map[string]any)) string {
	header := map[string]any{"alg": "ES256", "kid": "ABCDEF1234"}
	claims := map[string]any{"iss": "TEAMID1234", "sub": client, "aud": "https://appleid.apple.com", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	if modify != nil {
		modify(header, claims)
	}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	b64 := base64.RawURLEncoding
	return b64.EncodeToString(h) + "." + b64.EncodeToString(p) + "." + b64.EncodeToString(make([]byte, 64))
}

func TestProviderRuntimeAuthFailClosed(t *testing.T) {
	noDefaults := providerRuntimeSettings{
		env:        func(string) string { panic("inactive config read environment") },
		readSecret: func(string) (string, error) { panic("inactive config opened a secret") },
	}
	if a, err := providerRuntimeAuth(noDefaults); a != nil || err != nil {
		t.Fatal(a, err)
	}
	noNetwork := providerFakeRoundTrip(func(*http.Request) (*http.Response, error) { panic("startup attempted provider discovery") })
	env := map[string]string{
		"GOMAR_AUTH_PUBLIC_ORIGIN":                     "https://auth.example.com",
		"GOMAR_AUTH_PROVIDERS":                         "google,microsoft,microsoft-work",
		"GOMAR_AUTH_GOOGLE_CLIENT_ID":                  "synthetic-goog",
		"GOMAR_AUTH_GOOGLE_CLIENT_SECRET_FILE":         "/synthetic/google",
		"GOMAR_AUTH_MICROSOFT_CLIENT_ID":               "synthetic-ms",
		"GOMAR_AUTH_MICROSOFT_CLIENT_SECRET_FILE":      "/synthetic/microsoft",
		"GOMAR_AUTH_MICROSOFT_WORK_CLIENT_ID":          "synthetic-work",
		"GOMAR_AUTH_MICROSOFT_WORK_CLIENT_SECRET_FILE": "/synthetic/work",
		"GOMAR_AUTH_MICROSOFT_WORK_TENANT_ID":          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}
	calls := 0
	settings := providerRuntimeSettings{
		enabled: true, protectedIngress: true, authority: "auth.example.com", network: noNetwork,
		env: func(key string) string { return env[key] },
		readSecret: func(path string) (string, error) {
			calls++
			if strings.HasPrefix(path, "/synthetic/") {
				return "synthetic-secret", nil
			}
			return "", errors.New("private file path must not leak to logs")
		},
	}
	if a, err := providerRuntimeAuth(settings); a != nil || err != errProviderNeedsSchema || calls != 0 {
		t.Fatal(a, err, calls)
	}
	settings.migrations = []string{providerAuthMigration}
	settings.protectedIngress = false
	if a, err := providerRuntimeAuth(settings); a != nil || err != errProviderNeedsHTTPS || calls != 0 {
		t.Fatal(a, err, calls)
	}
	settings.protectedIngress = true
	env["GOMAR_AUTH_PUBLIC_ORIGIN"] = "https://other.example.com"
	if a, err := providerRuntimeAuth(settings); a != nil || err != errProviderNeedsHTTPS || calls != 0 {
		t.Fatal(a, err, calls)
	}
	env["GOMAR_AUTH_PUBLIC_ORIGIN"] = "https://auth.example.com"
	got, err := providerRuntimeAuth(settings)
	if err != nil || got == nil || len(got.providers) != 3 || calls != 3 {
		t.Fatal(got, err, calls)
	}
	for id, p := range got.providers {
		if p.config.RedirectURL != "https://auth.example.com/api/v1/auth/providers/"+id+"/callback" {
			t.Fatal("redirect does not use the trusted authority")
		}
	}
	env["GOMAR_AUTH_PROVIDERS"] = "google,apple,microsoft"
	before := calls
	if got, err := providerRuntimeAuth(settings); got != nil || err != errProviderConfig || calls != before {
		t.Fatal(got, err, calls-before)
	}
	for _, bad := range []string{"", "google,google", "google,", "google, microsoft", "unknown", "*"} {
		env["GOMAR_AUTH_PROVIDERS"] = bad
		if a, err := providerRuntimeAuth(settings); a != nil || err != errProviderConfig {
			t.Fatal(a, err)
		}
	}
	env["GOMAR_AUTH_PROVIDERS"] = "google"
	env["GOMAR_AUTH_GOOGLE_CLIENT_SECRET_FILE"] = "/should-not-leak"
	if a, err := providerRuntimeAuth(settings); a != nil || err != errProviderSecretFile {
		t.Fatal(a, err)
	}
}

func TestProviderSecretFilePrivateAndBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("synthetic-value\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := readProviderSecretFile(path); value != "synthetic-value" || err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if value, err := readProviderSecretFile(link); value != "" || err != errProviderSecretFile {
		t.Fatal(err)
	}
	if _, err := readProviderSecretFile("secret"); err != errProviderSecretFile {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readProviderSecretFile(path); err != errProviderSecretFile {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", strings.Repeat("a", 4097), "value\n\n", " two words"} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readProviderSecretFile(path); err != errProviderSecretFile {
			t.Fatal(err)
		}
	}
}

func TestAppleClientSecretOfflineShape(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	const service = "com.example.signin"
	if !validAppleClientSecretShape(providerAppleSecretFixture(service, now, nil), service, now) {
		t.Fatal("shape rejected")
	}
	for _, mutate := range []func(map[string]any, map[string]any){
		func(h, p map[string]any) { h["alg"] = "none" },
		func(h, p map[string]any) { p["sub"] = "other-service" },
		func(h, p map[string]any) { p["aud"] = "https://other.invalid" },
		func(h, p map[string]any) { p["exp"] = now.Add(time.Minute).Unix() },
		func(h, p map[string]any) { p["iat"] = now.Add(10 * time.Minute).Unix() },
		func(h, p map[string]any) { p["exp"] = now.Add(providerAppleMaxSecretAge + time.Minute).Unix() },
	} {
		if validAppleClientSecretShape(providerAppleSecretFixture(service, now, mutate), service, now) {
			t.Fatal("accepted wrong declared secret scope")
		}
	}
}
