package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNativeSignupDefaultsAndStrictJSON(t *testing.T) {
	const host = "127.0.0.1:8443"
	enabled := func() *nativeSignupHosted {
		return &nativeSignupHosted{Enabled: true, Origin: "https://" + host, Audience: "fixture-hosted",
			Resolver: func(context.Context, int64) (nativeSignupPlan, error) { return nativeSignupPlan{}, nil }}
	}
	a := &app{host: host, providerAuth: &hostedProviderAuth{host: host, origin: "https://" + host}}
	for _, disabled := range []*nativeSignupHosted{nil, {Enabled: true}, {Enabled: true, Origin: "https://" + host, Audience: "fixture-hosted"}} {
		a.nativeSignup = disabled
		for _, path := range []string{"/alta", "/api/v1/alta/revisar", "/api/v1/alta/confirmar"} {
			r := httptest.NewRequest("GET", "https://"+host+path, nil)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatalf("default/invalid settings exposed %s", path)
			}
		}
	}
	a.nativeSignup = enabled()
	if a.activeNativeSignup() == nil {
		t.Fatal("explicit loopback configuration with resolver was refused")
	}
	a.host, a.providerAuth.host, a.nativeSignup.Origin, a.providerAuth.origin =
		"example.invalid:8443", "example.invalid:8443", "https://example.invalid:8443", "https://example.invalid:8443"
	if a.activeNativeSignup() != nil {
		t.Fatal("non-loopback configuration exposed signup")
	}
	challenge := newProviderRandom()
	good := `{"challenge":"` + challenge + `","confirm":true}`
	for _, tc := range []struct {
		body             string
		confirm, allowed bool
	}{
		{`{}`, false, true}, {good, true, true},
		{`{"email":"fixture@example.invalid"}`, false, false},
		{`{"intent":"untrusted"}`, false, false},
		{`[]`, false, false}, {`null`, false, false}, {`{}{}`, false, false},
		{good + ` null`, true, false}, {`{"challenge":"` + challenge + `","Confirm":true}`, true, false},
		{`{"challenge":"` + challenge + `","confirm":false,"confirm":true}`, true, false},
		{`{"challenge":"` + challenge + `","confirm":true,"confi\u0072m":true}`, true, false},
		{`{"challenge":"` + challenge + `","challenge":"` + challenge + `","confirm":true}`, true, false},
		{`{"challenge":"` + challenge + `","confirm":true,"owner_id":2}`, true, false},
		{`{"challenge":"` + challenge + `","confirm":"true"}`, true, false},
		{`{"challenge":"` + challenge + `","confirm":null}`, true, false},
		{`{"challenge":"` + challenge + `","confirm":false}`, true, false},
		{`{"challenge":"` + strings.Repeat("a", 1050) + `","confirm":true}`, true, false},
	} {
		r := httptest.NewRequest("POST", "https://"+host+"/api/v1/alta/confirmar", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		input, err := readNativeSignupAction(httptest.NewRecorder(), r, tc.confirm)
		if (err == nil) != tc.allowed || tc.allowed && tc.confirm && (input.challenge != challenge || !input.confirm) {
			t.Fatalf("strict input mismatch for %q: %v", tc.body[:min(len(tc.body), 100)], err)
		}
	}
}

type signupTestPeer struct {
	t           *testing.T
	ctx         context.Context
	db          *sql.DB
	application *app
	server      *httptest.Server
	recovery    func(string) *http.Request
	owner       func(string, string) *http.Request
}

func signupFixture(t *testing.T, ctx context.Context, db *sql.DB) signupTestPeer {
	t.Helper()
	var router http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	host := server.Listener.Addr().String()
	origin := "https://" + host
	a := &app{host: host, db: db, providerAuth: &hostedProviderAuth{host: host, origin: origin}, enableMCP: true}
	ops := &app{host: host, db: db, adminRecovery: true}
	// All subjects and legal display labels are synthetic. Session cookies
	// still come from Gomar's exact verified provider-session entry point.
	presets := map[string]verifiedProviderIdentity{}
	actors := map[string]int64{}
	for _, name := range []string{"primary", "changed", "desk", "mobile", "legacy", "other", "staff"} {
		identity := verifiedProviderIdentity{"google", "https://accounts.google.com", "signup-fixture-" + name}
		presets[name] = identity
		r := httptest.NewRequest("GET", origin+"/__signup-fixture-sign-in/"+name, nil).WithContext(ctx)
		if err := a.startVerifiedProviderSession(httptest.NewRecorder(), r, identity); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := db.QueryRowContext(ctx, "SELECT user_id FROM gomar.provider_identities WHERE provider=$1 AND issuer=$2 AND subject=$3",
			identity.Provider, identity.Issuer, identity.Subject).Scan(&id); err != nil {
			t.Fatal(err)
		}
		actors[name] = id
	}
	execSQL := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(`INSERT INTO gomar.users(username,password,active,staff,superuser) VALUES('same-email@example.invalid','fixture',true,true,true);
		INSERT INTO gomar.entities(id,name,cuit,active) VALUES(711,'QA negocio operativo','00000000711',true);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES(711,711,'QA local permitido',true),(712,711,'QA local ajeno',true);`)
	execSQL(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active) VALUES(711,$1,711,'administration',true)`, actors["staff"])
	execSQL(`INSERT INTO gomar.outlet_grants VALUES(711,711)`)
	recovery := nativeOwnerRecoveryFixture(t, ctx, db, ops, origin)
	recoveryRequest := func(path string) *http.Request {
		r := httptest.NewRequest("POST", origin+path, nil).WithContext(context.WithValue(ctx, privateHTTPSKey{}, true))
		r.Header.Set("Origin", origin)
		for _, cookie := range recovery {
			r.AddCookie(cookie)
			if cookie.Name == adminRecoveryCSRF {
				r.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		return r
	}
	const audience = "signup-fixture-hosted"
	plans := map[int64]nativeSignupPlan{}
	for _, name := range []string{"primary", "changed", "desk", "mobile", "legacy"} {
		target := signupFixtureTarget(name)
		if err := ops.designateNativeOwner(recoveryRequest("/admin/__signup-fixture-designate"), target, actors[name]); err != nil {
			t.Fatal(err)
		}
		if _, err := ops.approveNativeOwnerDesignation(recoveryRequest("/admin/__signup-fixture-approve"), target); err != nil {
			t.Fatal(err)
		}
		plans[actors[name]] = nativeSignupPlan{Actor: actors[name], Target: target, Organization: "QA Organización  " + name,
			Branches: []nativeInstallBranch{{Ref: "outlet-b", Name: "Norte"}, {Ref: "outlet-a", Name: "Local  Centro"}}}
	}
	var lock sync.RWMutex
	original := map[string][]*http.Cookie{}
	a.nativeSignup = &nativeSignupHosted{Enabled: true, Origin: origin, Audience: audience,
		Resolver: func(_ context.Context, actor int64) (nativeSignupPlan, error) {
			lock.RLock()
			defer lock.RUnlock()
			p, ok := plans[actor]
			if !ok {
				return nativeSignupPlan{}, errors.New("unknown synthetic actor")
			}
			return p, nil
		}}
	configured := a.routes()
	disabled := (&app{host: host, db: db, providerAuth: a.providerAuth, enableMCP: true}).routes()
	mux := http.NewServeMux()
	mux.Handle("/", configured)
	mux.HandleFunc("GET /__signup-fixture-sign-in/{preset}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("preset")
		id, ok := presets[name]
		if !ok || !a.nativeSignupLocal(r, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		nativeSignupPrivateHeaders(w)
		if err := a.startVerifiedProviderSession(w, r, id); err != nil {
			failure(w, err)
			return
		}
		lock.Lock()
		if original[name] == nil {
			// Keep the first synthetic session in memory only so the new
			// fixture app can prove it uses the actual browser's session.
			original[name] = (&http.Response{Header: w.Header()}).Cookies()
		}
		lock.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /__signup-fixture-change-label", func(w http.ResponseWriter, r *http.Request) {
		if !a.nativeSignupLocal(r, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		lock.Lock()
		p := plans[actors["changed"]]
		p.Organization = "QA Etiqueta nueva"
		plans[p.Actor] = p
		lock.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /__signup-fixture-disabled", func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.URL.Path = "/alta"
		disabled.ServeHTTP(w, r)
	})
	router = mux
	server.StartTLS()
	ownerRequest := func(name, path string) *http.Request {
		r := httptest.NewRequest("POST", origin+path, nil).WithContext(ctx)
		r.Header.Set("Origin", origin)
		lock.RLock()
		defer lock.RUnlock()
		for _, cookie := range original[name] {
			r.AddCookie(cookie)
			if cookie.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		return r
	}
	return signupTestPeer{t: t, ctx: ctx, db: db, application: a, server: server, recovery: recoveryRequest, owner: ownerRequest}
}

func signupFixtureTarget(name string) nativeOwnerTarget {
	return nativeOwnerTarget{Intent: "signup-" + name, Business: "private-business-" + name, Audience: "signup-fixture-hosted",
		RequestRef: "private-request-" + name, Revision: 7, Outlets: []string{"outlet-a", "outlet-b"}}
}

func (f signupTestPeer) client() *http.Client {
	f.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Client{Transport: f.server.Client().Transport, Jar: jar}
}

func (f signupTestPeer) request(c *http.Client, method, path, body, origin string) (int, []byte, http.Header) {
	f.t.Helper()
	r, err := http.NewRequestWithContext(f.ctx, method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if method == "POST" {
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		base, _ := url.Parse(f.server.URL)
		for _, cookie := range c.Jar.Cookies(base) {
			if cookie.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
	}
	reply, err := c.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer reply.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(reply.Body, 100<<10))
	if err != nil {
		f.t.Fatal(err)
	}
	return reply.StatusCode, raw, reply.Header
}

func TestPostgresNativeSignup(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	f := signupFixture(t, ctx, db)
	main := f.client()
	get := func(c *http.Client, path string, want int) {
		t.Helper()
		code, _, _ := f.request(c, "GET", path, "", "")
		if code != want {
			t.Fatalf("GET %s: %d want %d", path, code, want)
		}
	}
	post := func(c *http.Client, path, body string, want int) []byte {
		t.Helper()
		code, raw, h := f.request(c, "POST", "/api/v1/alta/"+path, body, f.server.URL)
		if code != want {
			t.Fatalf("POST %s: %d want %d", path, code, want)
		}
		if h.Get("Cache-Control") != "private, no-store" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("signup response is not explicitly private")
		}
		if bytes.Contains(raw, []byte("private-request")) || bytes.Contains(raw, []byte("private-business")) ||
			bytes.Contains(raw, []byte("example.invalid")) || bytes.Contains(raw, []byte("accept-")) {
			t.Fatal("signup response disclosed an internal reference")
		}
		return raw
	}
	login := func(c *http.Client, name string) { get(c, "/__signup-fixture-sign-in/"+name, 204) }
	get(main, "/alta", 404)
	login(main, "primary")
	get(main, "/__signup-fixture-disabled", 404)
	get(main, "/alta?owner=other", 404)
	get(main, "/alta?", 404)
	get(main, "/alta", 200)
	post(main, "revisar", `{"email":"forged@example.invalid"}`, 400)
	code, _, _ := f.request(main, "POST", "/api/v1/alta/revisar", "{}", "https://unrelated.invalid")
	if code != 403 {
		t.Fatal("foreign Origin reached signup")
	}
	review := func(c *http.Client) nativeSignupReview {
		t.Helper()
		raw := post(c, "revisar", "{}", 200)
		var got nativeSignupReview
		var visible map[string]json.RawMessage
		if json.Unmarshal(raw, &got) != nil || json.Unmarshal(raw, &visible) != nil ||
			len(visible) != 3 || !providerRandom(got.Challenge) || got.Organization == "" || len(got.Branches) != 2 ||
			got.Branches[0] != "Local  Centro" || got.Branches[1] != "Norte" {
			t.Fatal("review did not show only the safe exact display")
		}
		return got
	}
	primary := review(main)
	confirm := func(challenge string) string { return `{"challenge":"` + challenge + `","confirm":true}` }
	other := f.client()
	login(other, "other")
	get(other, "/alta", 404)
	post(other, "revisar", "{}", 404)
	post(other, "confirmar", confirm(primary.Challenge), 404)
	sameActor := f.client()
	login(sameActor, "primary")
	post(sameActor, "confirmar", confirm(primary.Challenge), 404)
	changed := f.client()
	login(changed, "changed")
	oldProjection := review(changed)
	get(changed, "/__signup-fixture-change-label", 204)
	post(changed, "confirmar", confirm(oldProjection.Challenge), 404)
	if updated := review(changed); updated.Organization != "QA Etiqueta nueva" {
		t.Fatal("fixture label did not change")
	}
	post(main, "confirmar", `{"challenge":"`+primary.Challenge+`","confirm":false,"confirm":true}`, 400)
	post(main, "confirmar", confirm(primary.Challenge)+`{}`, 400)
	post(main, "confirmar", confirm(primary.Challenge), 204)
	post(main, "confirmar", confirm(primary.Challenge), 404)
	var confirmations, drafts, pending, memberships int
	err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.native_owner_receipts),
		(SELECT count(*) FROM gomar.native_owner_install_drafts),(SELECT count(*) FROM gomar.native_owner_pending_installs),
		(SELECT count(*) FROM gomar.memberships)`).Scan(&confirmations, &drafts, &pending, &memberships)
	if err != nil || confirmations != 1 || drafts+pending != 0 || memberships != 1 {
		t.Fatalf("unexpected signup effects: %d %d %d %d %v", confirmations, drafts, pending, memberships, err)
	}
	// The existing unbound internal path is never grandfathered into a
	// draft/install, even when the provider consent is still current.
	legacy := f.client()
	login(legacy, "legacy")
	oldTarget := signupFixtureTarget("legacy")
	old, err := f.application.previewNativeOwnerConsent(f.owner("legacy", "/__fixture-review"), oldTarget.Intent, oldTarget.Audience)
	if err != nil {
		t.Fatalf("legacy fixture preview: %v", err)
	}
	oldReceipt, err := f.application.confirmNativeOwnerConsent(f.owner("legacy", "/__fixture-confirm"),
		nativeOwnerConfirmation{Challenge: old.Challenge, Target: old.Target, Accept: true})
	if err != nil {
		t.Fatalf("legacy fixture confirmation: %v", err)
	}
	names := []string{"primary"}
	if testNativeSignupChrome(t, ctx, f) {
		names = append(names, "desk", "mobile")
	}
	f.server.Close()
	checkSignupDisplayAfterRestart(t, f, names, oldReceipt)
}

func testNativeSignupChrome(t *testing.T, ctx context.Context, f signupTestPeer) bool {
	t.Helper()
	script := os.Getenv("GOMAR_QA_SIGNUP_BROWSER_SCRIPT")
	if script == "" {
		return false
	} // The regular PostgreSQL suite has no browser toolchain.
	python, mcp := os.Getenv("GOMAR_QA_SIGNUP_PYTHON"), os.Getenv("GOMAR_QA_SIGNUP_MCP")
	if !filepath.IsAbs(script) || !filepath.IsAbs(python) || !filepath.IsAbs(mcp) {
		t.Fatal("expected exact isolated QA commands")
	}
	scratch := t.TempDir()
	cert := filepath.Join(scratch, "fixture.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, python, "-B", script, f.server.URL, mcp, cert, scratch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated Chrome/packaged MCP failed: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("PASS: native signup desktop/mobile and packaged MCP")) {
		t.Fatal("expected actual Chrome and MCP pass marker")
	}
	var receipts, drafts, pending, memberships int
	if err = f.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.native_owner_receipts),
		(SELECT count(*) FROM gomar.native_owner_install_drafts),(SELECT count(*) FROM gomar.native_owner_pending_installs),
		(SELECT count(*) FROM gomar.memberships)`).Scan(&receipts, &drafts, &pending, &memberships); err != nil || receipts != 4 || drafts+pending != 0 || memberships != 1 {
		t.Fatal("unexpected real browser effects")
	}
	t.Log(strings.TrimSpace(string(output)))
	return true
}
