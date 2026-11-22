package main

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSignInModeRequiresReadyRecoveryAndAllProviders(t *testing.T) {
	ready := []string{providerAuthMigration, adminRecoveryMigration}
	if err := validateSignInMode(false, false, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateSignInMode(true, false, false, ready, nil); err != errAdminRecoveryHTTPS {
		t.Fatal(err)
	}
	if err := validateSignInMode(true, false, true, ready[:1], nil); err != errAdminRecoverySchema {
		t.Fatal(err)
	}
	if err := validateSignInMode(false, true, true, ready, nil); err != errProviderOnlyUnsafe {
		t.Fatal(err)
	}
	auth := &hostedProviderAuth{providers: map[string]*providerOIDC{"google": {}, "apple": {}, "microsoft-work": {}}}
	if err := validateSignInMode(true, true, true, ready, auth); err != errProviderOnlyUnsafe {
		t.Fatal(err)
	}
	auth.providers["microsoft"] = &providerOIDC{}
	if err := validateSignInMode(true, true, true, ready[1:], auth); err != errProviderOnlyUnsafe {
		t.Fatal(err)
	}
	if err := validateSignInMode(true, true, true, ready, auth); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAdminRecoveryAndProviderOnlyRealm(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgresThrough(ctx, db, postgresMigrationNames[:12]); err != nil {
		t.Fatal(err)
	}
	if err := verifyLocalAdminRecoveryAccount(ctx, db); err == nil {
		t.Fatal("allowed transition with no recovery account")
	}
	const password = "local-synthetic-admin-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(username,password,active,staff,superuser) VALUES
		('root-fixture',$1,TRUE,TRUE,TRUE),('staff-fixture',$1,TRUE,TRUE,FALSE),
		('disabled-fixture',$1,FALSE,TRUE,TRUE),('nostaff-fixture',$1,TRUE,FALSE,TRUE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if err := verifyLocalAdminRecoveryAccount(ctx, db); err != nil {
		t.Fatal(err)
	}
	const origin = "https://example.com"
	classic := (&app{db: db, host: "example.com", enableMCP: true}).routes()
	type jar map[string]*http.Cookie
	request := func(h http.Handler, method, path, contentType, body, source string, cookies jar) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body)).WithContext(ctx)
		if source != "" {
			r.Header.Set("Origin", source)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	update := func(c jar, w *httptest.ResponseRecorder) {
		t.Helper()
		for _, cookie := range w.Result().Cookies() {
			if cookie.MaxAge < 0 {
				delete(c, cookie.Name)
			} else {
				c[cookie.Name] = cookie
			}
		}
	}
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got %d want %d", w.Code, status)
		}
	}
	classicCookies := jar{}
	update(classicCookies, request(classic, "GET", "/api/v1/auth/csrf", "", "", "", nil))
	loginBody := `{"usuario":"root-fixture","clave":"` + password + `"}`
	w := request(classic, "POST", "/api/v1/auth/login", "application/json", loginBody, origin, classicCookies)
	expect(w, 200)
	update(classicCookies, w)
	if classicCookies["sessionid"] == nil {
		t.Fatal("default local login stopped working")
	}
	expect(request(classic, "GET", "/admin/", "", "", "", classicCookies), 200)

	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := verifyPostgresMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	expect(request(classic, "GET", "/api/v1/auth/me", "", "", "", classicCookies), 200)
	var method string
	if err := db.QueryRowContext(ctx, "SELECT auth_method FROM sessions").Scan(&method); err != nil || method != "password" {
		t.Fatal(method, err)
	}

	rehearsal := (&app{db: db, host: "example.com", adminRecovery: true, enableMCP: true}).routes()
	expect(request(rehearsal, "GET", "/api/v1/auth/me", "", "", "", classicCookies), 200)
	expect(request(rehearsal, "GET", "/admin/", "", "", "", classicCookies), 302)
	a := &app{db: db, host: "example.com", adminRecovery: true, providerOnly: true, enableMCP: true}
	router := a.routes()
	expect(request(router, "POST", "/api/v1/auth/login", "application/json", loginBody, origin, classicCookies), 403)
	expect(request(router, "GET", "/api/v1/auth/me", "", "", "", classicCookies), 401)
	expect(request(router, "GET", "/admin/", "", "", "", classicCookies), 302)

	w = httptest.NewRecorder()
	p := httptest.NewRequest("GET", origin+"/synthetic-verified-provider", nil).WithContext(ctx)
	if err := a.startVerifiedProviderSession(w, p, verifiedProviderIdentity{"google", "https://accounts.google.com", "isolated-fixture"}); err != nil {
		t.Fatal(err)
	}
	providerCookies := jar{}
	update(providerCookies, w)
	expect(request(router, "GET", "/api/v1/auth/me", "", "", "", providerCookies), 200)
	expect(request(router, "GET", "/admin/", "", "", "", providerCookies), 302)
	var providerSessions int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE auth_method='provider'").Scan(&providerSessions); err != nil || providerSessions != 1 {
		t.Fatal(providerSessions, err)
	}

	adminCookies := jar{}
	update(adminCookies, request(router, "GET", "/admin/login/", "", "", "", nil))
	c := adminCookies[adminRecoveryCSRF]
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/admin/" || adminCookies["csrftoken"] != nil {
		t.Fatal("bad admin CSRF cookie")
	}
	form := func(name, pw, csrf string) string {
		return url.Values{"username": {name}, "password": {pw}, "csrfmiddlewaretoken": {csrf}}.Encode()
	}
	const formType = "application/x-www-form-urlencoded"
	expect(request(router, "POST", "/admin/login/", formType, form("root-fixture", password, c.Value), "https://other.invalid", adminCookies), 403)
	expect(request(router, "POST", "/admin/login/", formType, form("root-fixture", password, classicCookies["csrftoken"].Value), origin, classicCookies), 403)
	for _, name := range []string{"staff-fixture", "disabled-fixture", "nostaff-fixture"} {
		w := request(router, "POST", "/admin/login/", formType, form(name, password, c.Value), origin, adminCookies)
		expect(w, 200)
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == adminRecoveryCookie {
				t.Fatal("wrong role obtained recovery cookie")
			}
		}
	}
	w = request(router, "POST", "/admin/login/", formType, form("root-fixture", password, c.Value), origin, adminCookies)
	expect(w, 302)
	update(adminCookies, w)
	raw := adminCookies[adminRecoveryCookie]
	if raw == nil || !raw.Secure || !raw.HttpOnly || raw.Path != "/admin/" || raw.SameSite != http.SameSiteStrictMode ||
		adminCookies["sessionid"] != nil || adminCookies["csrftoken"] != nil {
		t.Fatal("recovery emitted normal-session credentials")
	}
	expect(request(router, "GET", "/admin/", "", "", "", adminCookies), 200)
	expect(request(router, "GET", "/api/v1/auth/me", "", "", "", adminCookies), 401)
	expect(request(router, "GET", "/api/v1/entities", "", "", "", adminCookies), 401)
	copiedRecovery := jar{adminRecoveryCookie: raw, "csrftoken": providerCookies["csrftoken"]}
	expect(request(router, "POST", "/api/v1/auth/mcp-token", "application/json", `{"entity_id":1}`, origin, copiedRecovery), 401)
	sessions := 0
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&sessions); err != nil || sessions != 2 {
		t.Fatal(sessions, err)
	}

	newPassword := "Different-private-fixture-4!"
	change := url.Values{"old_password": {password}, "new_password1": {newPassword}, "new_password2": {newPassword},
		"csrfmiddlewaretoken": {adminCookies[adminRecoveryCSRF].Value}}
	w = request(router, "POST", adminPasswordPath, formType, change.Encode(), origin, adminCookies)
	expect(w, 302)
	if w.Header().Get("Location") != adminPasswordDone {
		t.Fatal("password-change failed to rotate realm")
	}
	update(adminCookies, w)
	if adminCookies[adminRecoveryCookie].Value == raw.Value {
		t.Fatal("recovery token not rotated")
	}
	expect(request(router, "GET", "/admin/", "", "", "", jar{adminRecoveryCookie: raw}), 302)
	expect(request(router, "GET", adminPasswordDone, "", "", "", adminCookies), 200)
	w = request(router, "POST", "/admin/logout/", formType,
		url.Values{"csrfmiddlewaretoken": {adminCookies[adminRecoveryCSRF].Value}}.Encode(), origin, adminCookies)
	expect(w, 200)
	if !strings.Contains(w.Body.String(), "Sesión cerrada") {
		t.Fatal("logout not rendered")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM admin_recovery_sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal(sessions, err)
	}
	update(adminCookies, w)
	if adminCookies[adminRecoveryCookie] != nil || adminCookies[adminRecoveryCSRF] != nil {
		t.Fatal("admin cookies did not clear")
	}
	expect(request(router, "GET", "/api/v1/auth/me", "", "", "", providerCookies), 200)

	update(adminCookies, request(router, "GET", "/admin/login/", "", "", "", nil))
	w = request(router, "POST", "/admin/login/", formType, form("root-fixture", newPassword, adminCookies[adminRecoveryCSRF].Value), origin, adminCookies)
	expect(w, 302)
	update(adminCookies, w)
	hash := sha256.Sum256([]byte(adminCookies[adminRecoveryCookie].Value))
	if _, err := db.ExecContext(ctx, "UPDATE admin_recovery_sessions SET expires=$1 WHERE token_hash=$2", time.Now().Add(-time.Second).Unix(), hash[:]); err != nil {
		t.Fatal(err)
	}
	w = request(router, http.MethodGet, "/admin/", "", "", "", adminCookies)
	expect(w, 302)
	update(adminCookies, w)
	if adminCookies[adminRecoveryCookie] != nil {
		t.Fatal("expired recovery cookie not cleared")
	}
	w = request(router, "POST", "/admin/logout/", formType,
		url.Values{"csrfmiddlewaretoken": {adminCookies[adminRecoveryCSRF].Value}}.Encode(), origin, adminCookies)
	expect(w, 200)
	update(adminCookies, w)
	if adminCookies[adminRecoveryCSRF] != nil {
		t.Fatal("expired recovery logout did not clear CSRF")
	}
}
