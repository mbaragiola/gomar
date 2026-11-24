package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNativeSignupDefaultsAndStrictJSON(t *testing.T) {
	const host = "127.0.0.1:8443"
	enabled := &nativeSignupHosted{Enabled: true, Origin: "https://" + host, Audience: "fixture-hosted"}
	a := &app{host: host, db: new(sql.DB), providerAuth: &hostedProviderAuth{host: host, origin: "https://" + host}}
	for _, disabled := range []*nativeSignupHosted{nil, {Enabled: true}, {Enabled: true, Origin: "https://" + host}} {
		a.nativeSignup = disabled
		for _, path := range []string{"/alta", nativeSignupStatusPath, nativeSignupReviewPath, nativeSignupConfirmPath} {
			method := http.MethodPost
			if path == "/alta" {
				method = http.MethodGet
			}
			r := httptest.NewRequest(method, "https://"+host+path, nil)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatalf("default/invalid settings exposed %s", path)
			}
		}
	}
	a.nativeSignup = enabled
	if a.activeNativeSignup() == nil {
		t.Fatal("explicit loopback configuration was refused")
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
	ops         *app
	actors      map[string]int64
	configure   func(*sql.DB) *app
	server      *httptest.Server
	recovery    func(string) *http.Request
	owner       func(string, string) *http.Request
}

func signupFixture(t *testing.T, ctx context.Context, db *sql.DB) signupTestPeer {
	t.Helper()
	var router http.Handler
	var routerLock sync.RWMutex
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routerLock.RLock()
		defer routerLock.RUnlock()
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
	for _, name := range []string{"primary", "changed", "desk", "mobile", "legacy"} {
		target := signupFixtureTarget(name)
		if err := ops.designateNativeOwner(recoveryRequest("/admin/__signup-fixture-designate"), target, actors[name]); err != nil {
			t.Fatal(err)
		}
		if _, err := ops.approveNativeOwnerDesignation(recoveryRequest("/admin/__signup-fixture-approve"), target); err != nil {
			t.Fatal(err)
		}
		if name != "legacy" {
			if err := ops.recordNativeSignupPlan(recoveryRequest("/admin/__signup-fixture-plan"), target, signupFixtureDisplay(name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	var lock sync.RWMutex
	original := map[string][]*http.Cookie{}
	configure := func(pool *sql.DB) *app {
		a := &app{host: host, db: pool, providerAuth: &hostedProviderAuth{host: host, origin: origin}, enableMCP: true,
			nativeSignup: &nativeSignupHosted{Enabled: true, Origin: origin, Audience: audience}}
		disabled := (&app{host: host, db: pool, providerAuth: a.providerAuth, enableMCP: true}).routes()
		mux := http.NewServeMux()
		mux.Handle("/", a.routes())
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
				// Keep the first synthetic session in memory only so a new
				// fixture app can prove it uses that browser's exact session.
				original[name] = (&http.Response{Header: w.Header()}).Cookies()
			}
			lock.Unlock()
			w.WriteHeader(204)
		})
		mux.HandleFunc("GET /__signup-fixture-disabled", func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.URL.Path = "/alta"
			disabled.ServeHTTP(w, r)
		})
		routerLock.Lock()
		router = mux
		routerLock.Unlock()
		return a
	}
	a = configure(db)
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
	return signupTestPeer{t: t, ctx: ctx, db: db, application: a, ops: ops, actors: actors,
		configure: configure, server: server, recovery: recoveryRequest, owner: ownerRequest}
}

func (f *signupTestPeer) restartApp() {
	f.t.Helper()
	db, err := connectPostgres(f.ctx, os.Getenv("GOMAR_LOCAL_POSTGRES_TEST_DSN"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { db.Close() })
	f.application = f.configure(db)
	f.ops = &app{host: f.application.host, db: db, adminRecovery: true}
	if err = f.db.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.db = db
}

func signupFixtureDisplay(name string) nativeSignupPlan {
	return nativeSignupPlan{Organization: "QA Organización  " + name,
		Branches: []nativeInstallBranch{{Ref: "outlet-b", Name: "Norte"}, {Ref: "outlet-a", Name: "Local  Centro"}}}
}

func (f signupTestPeer) designateAndApprove(name string, target nativeOwnerTarget) string {
	f.t.Helper()
	if err := f.ops.designateNativeOwner(f.recovery("/admin/__signup-fixture-designate"), target, f.actors[name]); err != nil {
		f.t.Fatal(err)
	}
	ref, err := f.ops.approveNativeOwnerDesignation(f.recovery("/admin/__signup-fixture-approve"), target)
	if err != nil {
		f.t.Fatal(err)
	}
	return ref
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
	if string(bytes.TrimSpace(post(main, "estado", "{}", 200))) != `{"state":"review"}` {
		t.Fatal("pre-confirmation status exposed data or issued a preview")
	}
	post(main, "estado", `{"intent":"private-foreign"}`, 400)
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
	f.restartApp()
	db = f.db
	get(main, "/alta", 200)
	if restarted := review(main); restarted.Organization != primary.Organization || restarted.Challenge == primary.Challenge {
		t.Fatal("protected display did not survive the SQL-backed fixture restart")
	}
	t.Log("persisted recovery plan and original challenge survived application/pool restart")
	confirm := func(challenge string) string { return `{"challenge":"` + challenge + `","confirm":true}` }
	other := f.client()
	login(other, "other")
	get(other, "/alta", 404)
	post(other, "estado", "{}", 404)
	post(other, "revisar", "{}", 404)
	post(other, "confirmar", confirm(primary.Challenge), 404)
	sameActor := f.client()
	login(sameActor, "primary")
	post(sameActor, "confirmar", confirm(primary.Challenge), 404)
	changed := f.client()
	login(changed, "changed")
	oldProjection := review(changed)
	supersession := signupFixtureTarget("changed")
	supersession.Revision++
	supersession.RequestRef += "-next"
	f.designateAndApprove("changed", supersession)
	get(changed, "/alta", 404) // never fall back to the older authored plan
	post(changed, "confirmar", confirm(oldProjection.Challenge), 404)
	changedDisplay := signupFixtureDisplay("changed")
	changedDisplay.Organization = "QA Etiqueta nueva"
	if err := f.ops.recordNativeSignupPlan(f.recovery("/admin/__signup-fixture-plan"), supersession, changedDisplay); err != nil {
		t.Fatal(err)
	}
	post(changed, "confirmar", confirm(oldProjection.Challenge), 404)
	if updated := review(changed); updated.Organization != "QA Etiqueta nueva" {
		t.Fatal("fixture label did not change")
	}
	post(main, "confirmar", `{"challenge":"`+primary.Challenge+`","confirm":false,"confirm":true}`, 400)
	post(main, "confirmar", confirm(primary.Challenge)+`{}`, 400)
	post(main, "confirmar", confirm(primary.Challenge), 204)
	f.restartApp()
	db = f.db
	get(main, "/alta", 200)
	checkRecorded := func(c *http.Client, want nativeSignupState) {
		t.Helper()
		var got nativeSignupStatus
		var fields map[string]json.RawMessage
		raw := post(c, "estado", "{}", 200)
		if json.Unmarshal(raw, &got) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 3 ||
			got.State != want || got.Organization != primary.Organization ||
			!slices.Equal(got.Branches, primary.Branches) {
			t.Fatal("original status returned anything outside its exact safe projection")
		}
	}
	checkRecorded(main, nativeSignupConfirmed)
	get(sameActor, "/alta", 404)
	post(sameActor, "estado", "{}", 404)
	post(main, "revisar", "{}", 404)
	post(main, "confirmar", confirm(primary.Challenge), 404)
	code, _, _ = f.request(main, "POST", nativeSignupStatusPath, "{}", "https://unrelated.invalid")
	if code != 403 {
		t.Fatal("foreign Origin reached state")
	}
	competitor := signupFixtureTarget("primary-competing")
	approval := f.designateAndApprove("primary", competitor)
	if err := f.ops.recordNativeSignupPlan(f.recovery("/admin/__signup-fixture-plan"), competitor, signupFixtureDisplay("foreign-display")); err != nil {
		t.Fatal(err)
	}
	get(main, "/alta", 404)
	post(main, "estado", "{}", 404)
	post(main, "revisar", "{}", 404)
	post(main, "confirmar", confirm(primary.Challenge), 404)
	if err := f.ops.revokeNativeOwnerApproval(f.recovery("/admin/__signup-fixture-revoke"), competitor, approval); err != nil {
		t.Fatal(err)
	}
	checkRecorded(main, nativeSignupConfirmed)
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
	testNativeSignupChrome(t, ctx, &f)
	checkSignupDisplayAfterRestart(t, f, []string{"primary"}, oldReceipt)
	f.restartApp()
	get(main, "/alta", 200)
	snapshot := func() (state [8]int) {
		t.Helper()
		if err := f.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.native_owner_previews),
			(SELECT count(*) FROM gomar.native_owner_receipts),(SELECT count(*) FROM gomar.native_owner_install_drafts),
			(SELECT count(*) FROM gomar.native_owner_pending_installs),(SELECT count(*) FROM gomar.native_owner_pending_branches),
			(SELECT count(*) FROM gomar.entities),(SELECT count(*) FROM gomar.memberships),(SELECT count(*) FROM gomar.outlet_grants)`).
			Scan(&state[0], &state[1], &state[2], &state[3], &state[4], &state[5], &state[6], &state[7]); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	checkRecorded(main, nativeSignupPrepared)
	checkRecorded(main, nativeSignupPrepared)
	get(sameActor, "/alta", 404)
	post(sameActor, "estado", "{}", 404)
	post(other, "estado", "{}", 404)
	post(main, "revisar", "{}", 404)
	post(main, "confirmar", confirm(primary.Challenge), 404)
	if snapshot() != before {
		t.Fatal("status or denied repeat changed native or access state")
	}
	t.Log("original-session recorded and prepared status survived restart; repeated/foreign checks wrote nothing")
}

func testNativeSignupChrome(t *testing.T, ctx context.Context, f *signupTestPeer) {
	t.Helper()
	script := os.Getenv("GOMAR_QA_SIGNUP_BROWSER_SCRIPT")
	if script == "" {
		return
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
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(ctx, python, "-B", script, f.server.URL, mcp, cert, scratch)
	type runResult struct {
		output []byte
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		output, err := cmd.CombinedOutput()
		done <- runResult{output: output, err: err}
	}()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	// The Python fixture can signal only these two synthetic gates in its
	// test-owned directory. It sends no session, private identifiers, or
	// approval. Go drives protected setup separately, then replaces the app
	// and connection pool before Chrome reloads its original session.
	for _, candidate := range []struct{ actor, cuit string }{{"desk", "00000009121"}, {"mobile", "00000009806"}} {
		ready := filepath.Join(scratch, "ready-"+candidate.actor)
		waiting := true
		for waiting {
			select {
			case result := <-done:
				t.Fatalf("Chrome ended before prepared-state proof: %v\n%s", result.err, result.output)
			case <-ctx.Done():
				t.Fatal("Chrome did not signal the scoped fixture")
			case <-tick.C:
				if _, err := os.Stat(ready); os.IsNotExist(err) {
					continue
				} else if err != nil {
					t.Fatal(err)
				}
				stageSignupBrowserFixture(t, f, candidate.actor, candidate.cuit)
				f.restartApp()
				if err := os.WriteFile(filepath.Join(scratch, "staged-"+candidate.actor), nil, 0600); err != nil {
					t.Fatal(err)
				}
				waiting = false
			}
		}
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("isolated Chrome/packaged MCP failed: %v\n%s", result.err, result.output)
	}
	output := result.output
	if !bytes.Contains(output, []byte("PASS: native signup desktop/mobile and packaged MCP")) {
		t.Fatal("expected actual Chrome and MCP pass marker")
	}
	var receipts, drafts, pending, memberships int
	if err := f.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.native_owner_receipts),
		(SELECT count(*) FROM gomar.native_owner_install_drafts),(SELECT count(*) FROM gomar.native_owner_pending_installs),
		(SELECT count(*) FROM gomar.memberships)`).Scan(&receipts, &drafts, &pending, &memberships); err != nil || receipts != 4 || drafts != 2 || pending != 2 || memberships != 1 {
		t.Fatal("unexpected real browser effects")
	}
	t.Log(strings.TrimSpace(string(output)))
}

func stageSignupBrowserFixture(t *testing.T, f *signupTestPeer, name, cuit string) {
	t.Helper()
	target := signupFixtureTarget(name)
	var receipt, approval string
	if err := f.db.QueryRowContext(f.ctx, `SELECT receipt_ref,approval_ref FROM gomar.native_owner_receipts WHERE intent=$1`,
		target.Intent).Scan(&receipt, &approval); err != nil {
		t.Fatal(err)
	}
	draft := signupFixtureInstallDraft(name, target, receipt, cuit)
	if err := f.ops.approveNativeInstallDraft(f.recovery("/admin/__signup-fixture-draft"), draft); err != nil {
		t.Fatal(err)
	}
	peer := nativeInstallPeer{Issuer: "billing-qa", Recipient: "gomar-qa"}
	record, err := f.application.installNativeOwnerDormant(f.owner(name, "/__signup-fixture-dormant"), target, receipt, peer,
		func(_ context.Context, got nativeInstallRequest) (nativeInstallObservation, error) {
			want := nativeInstallRequest{Peer: peer, Purpose: nativeInstallPurpose, Schema: 1, Target: target, Nonce: got.Nonce,
				Initiator: draft.Initiator, Claimant: draft.Claimant, ConsentRef: receipt, ApprovalRef: approval}
			if !providerRandom(got.Nonce) || !sameNativeInstallRequest(got, want) {
				return nativeInstallObservation{}, nativeOwnerConsentUnavailable
			}
			now := nativeOwnerNow()
			return nativeInstallObservation{Binding: want, CheckedAt: now, ExpiresAt: now.Add(2 * time.Second)}, nil
		})
	if err != nil || !record.Dormant {
		t.Fatalf("protected fixture did not record a dormant plan: %v", err)
	}
}
