package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresConfigRequiresOneVerifiedEndpoint(t *testing.T) {
	for _, dsn := range []string{
		"", "postgres://gomar@example.invalid/db?sslmode=disable",
		"postgres://gomar@example.invalid/db?sslmode=prefer",
		"postgres://gomar@example.invalid,other.invalid/db?sslmode=verify-full",
	} {
		if _, err := postgresConfig(dsn); err == nil {
			t.Fatalf("accepted unsafe connection settings: %q", dsn)
		}
	}
	for _, dsn := range []string{
		"host=/var/run/postgresql dbname=gomar user=gomar sslmode=disable",
		"postgres://gomar@example.invalid/db?sslmode=verify-full",
	} {
		cfg, err := postgresConfig(dsn)
		if err != nil || cfg.RuntimeParams["search_path"] != "pg_catalog, gomar" || cfg.RuntimeParams["timezone"] != "UTC" {
			t.Fatalf("unexpected PostgreSQL config for %q: %v", dsn, err)
		}
	}
}

func TestPostgresStateRejectsSQLite(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "margot.sqlite3"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preparePostgresState(state); err == nil || !strings.Contains(err.Error(), "separate from SQLite") {
		t.Fatalf("mixed state allowed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "private")); !os.IsNotExist(err) {
		t.Fatalf("unexpected change to SQLite state: %v", err)
	}
}

func testPostgresDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("GOMAR_LOCAL_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("requires a fresh disposable local PostgreSQL database")
	}
	cfg, err := postgresConfig(dsn)
	if err != nil || !filepath.IsAbs(cfg.Host) || !strings.HasPrefix(cfg.Database, "gomar_qa_") {
		t.Fatal("use a private local Unix socket and a gomar_qa_ database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := connectPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return ctx, db
}

func postgresTestCookies(t *testing.T, handler http.Handler, username, password string) []*http.Cookie {
	t.Helper()
	const origin = "http://example.com"
	csrf := httptest.NewRecorder()
	handler.ServeHTTP(csrf, httptest.NewRequest(http.MethodGet, origin+"/api/v1/auth/csrf", nil))
	if csrf.Code != http.StatusOK {
		t.Fatalf("prepare login: %d %s", csrf.Code, csrf.Body)
	}
	payload, err := json.Marshal(map[string]string{"usuario": username, "clave": password})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/auth/login", strings.NewReader(string(payload)))
	for _, c := range csrf.Result().Cookies() {
		r.AddCookie(c)
		if c.Name == "csrftoken" {
			r.Header.Set("X-CSRFToken", c.Value)
		}
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, r)
	if out.Code != http.StatusOK {
		t.Fatalf("login: %d %s", out.Code, out.Body)
	}
	cookies := out.Result().Cookies()
	for _, cookie := range csrf.Result().Cookies() {
		found := false
		for _, current := range cookies {
			found = found || current.Name == cookie.Name
		}
		if !found {
			cookies = append(cookies, cookie)
		}
	}
	return cookies
}

func TestPostgresMigrationRunsOnceAcrossConnections(t *testing.T) {
	ctx, db := testPostgresDB(t)
	var hasSchema bool
	if err := db.QueryRowContext(ctx, `SELECT to_regnamespace('gomar') IS NOT NULL`).Scan(&hasSchema); err != nil || hasSchema {
		t.Fatalf("requires an empty QA database: schema present=%t, error=%v", hasSchema, err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- migratePostgres(ctx, db) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM gomar.schema_migrations`).Scan(&count); err != nil || count != len(postgresMigrationNames) {
		t.Fatalf("migration records = %d, error = %v", count, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE gomar.schema_migrations SET digest = repeat('0',64) WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if err := migratePostgres(ctx, db); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected a migration mismatch, got %v", err)
	}
	script, err := postgresMigrations.ReadFile("postgres_migrations/" + postgresMigrationNames[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE gomar.schema_migrations SET digest=$1 WHERE version=1`, fmt.Sprintf("%x", sha256.Sum256(script))); err != nil {
		t.Fatal(err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatalf("reopen restored migration: %v", err)
	}
}
