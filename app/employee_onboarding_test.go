package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProviderAndEmployeeFlagCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		preview, only, hybrid, oidc, admin bool
		accept                             bool
	}{
		{"compatible", true, false, false, false, false, true},
		{"provider-only", false, true, false, true, true, true},
		{"provider-only-with-preview", true, true, false, true, true, false},
		{"conflicting-strict-modes", false, true, true, true, true, false},
		{"hybrid", false, false, true, true, true, true},
		{"hybrid-without-provider", false, false, true, false, true, false},
		{"hybrid-without-recovery", false, false, true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := employeeSignInFlags(tc.preview, tc.only, tc.hybrid, tc.oidc, tc.admin)
			if (err == nil) != tc.accept {
				t.Fatal("unexpected sign-in flags result", err)
			}
		})
	}
}

func TestPostgresEmployeeOnboarding(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const manager = "Synthetic owner test pass"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(username,password,active,staff,superuser) VALUES('fixture-manager',$1,true,false,false),('second-manager',$1,true,false,false),('ordinary-reader',$1,true,false,false)`, encodePassword(manager)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
  INSERT INTO entities(id,name,cuit,active) VALUES(1,'Org uno','00000000001',true),(2,'Org dos','00000000002',true);
  INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',true),(2,2,'Norte',true),(3,1,'Sin acceso',true),(4,1,'Cerrado',false);
  INSERT INTO memberships(id,user_id,entity_id,role,active) SELECT 501,id,1,'administration',true FROM users WHERE username='fixture-manager';
  INSERT INTO memberships(id,user_id,entity_id,role,active) SELECT 502,id,2,'administration',true FROM users WHERE username='second-manager';
  INSERT INTO memberships(id,user_id,entity_id,role,active) SELECT 503,id,1,'read_only',true FROM users WHERE username='ordinary-reader';
  INSERT INTO outlet_grants VALUES(501,1),(501,4),(502,2),(503,1);`); err != nil {
		t.Fatal(err)
	}
	enabled := (&app{db: db, host: "example.com", employeeOnboarding: true, enableMCP: true, enablePOS: true, enablePOSTableChecks: true, enablePOSManualTender: true}).routes()
	owner := postgresTestCookies(t, enabled, "fixture-manager", manager)
	other := postgresTestCookies(t, enabled, "second-manager", manager)
	reader := postgresTestCookies(t, enabled, "ordinary-reader", manager)
	const base = "/api/v1/entities/1/employees"
	serve := func(h http.Handler, method, path, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		origin := "http://example.com"
		if strings.HasPrefix(path, "/admin/") {
			origin = "https://example.com"
		}
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got %d want %d", w.Code, status)
		}
	}
	call := func(method, path, body string, cookies []*http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		out := serve(enabled, method, path, body, cookies)
		check(out, status)
		return out
	}
	fresh := func() []*http.Cookie { return call("GET", "/api/v1/auth/csrf", "", nil, 200).Result().Cookies() }
	good := `{"username":"Caja.Employee","role":"purchasing","outlet_ids":[1]}`
	check(serve((&app{db: db, host: "example.com"}).routes(), "POST", base, good, owner), 404)
	call("POST", base, good, reader, 403)
	call("POST", base, good, other, 404)
	call("POST", base, strings.Replace(good, "purchasing", "administration", 1), owner, 403)
	call("POST", base, strings.Replace(good, "purchasing", "unknown", 1), owner, 403)
	call("POST", base, strings.Replace(good, "[1]", "[1,1]", 1), owner, 422)
	for _, outlets := range []string{"[2]", "[3]", "[4]"} {
		call("POST", base, strings.Replace(good, "[1]", outlets, 1), owner, 404)
	}
	decodeInvite := func(w *httptest.ResponseRecorder) employeeInviteReply {
		var p employeeInviteReply
		if w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.ID == 0 || len(p.TemporaryPassword) != 32 {
			t.Fatal("invalid private invite receipt")
		}
		return p
	}
	first := decodeInvite(call("POST", base, good, owner, 201))
	call("POST", base, good, owner, 409)
	listing := call("GET", base, "", owner, 200)
	if strings.Contains(listing.Body.String(), first.TemporaryPassword) || !strings.Contains(listing.Body.String(), first.Username) {
		t.Fatal("wrong employee list projection")
	}
	call("GET", base, "", other, 404)
	second := decodeInvite(call("POST", "/api/v1/entities/2/employees", strings.Replace(good, "[1]", "[2]", 1), other, 201))
	if first.ID == second.ID || first.Username != second.Username {
		t.Fatal("org-scoped username not isolated")
	}
	// A verified provider identity with explicit organization access can
	// run the native employee handoff in compatible mode. A provider identity
	// without a grant cannot, and provider-only mode still has no employee routes.
	appWithEmployees := &app{db: db, host: "example.com", employeeOnboarding: true}
	provider := httptest.NewRecorder()
	providerRequest := httptest.NewRequest("GET", "http://example.com/verified-provider-fixture", nil).WithContext(ctx)
	if err := appWithEmployees.startVerifiedProviderSession(provider, providerRequest,
		verifiedProviderIdentity{"google", "https://accounts.google.com", "employee-owner-fixture"}); err != nil {
		t.Fatal(err)
	}
	providerCookies := provider.Result().Cookies()
	call("GET", "/api/v1/auth/me", "", providerCookies, 200)
	call("GET", base, "", providerCookies, 404)
	if _, err := db.ExecContext(ctx, `INSERT INTO memberships(id, user_id, entity_id, role, active)
	  SELECT 504,user_id,1,'administration',true FROM provider_identities WHERE subject='employee-owner-fixture';
	  INSERT INTO outlet_grants VALUES (504,1);`); err != nil {
		t.Fatal(err)
	}
	providerInvite := strings.Replace(good, "Caja.Employee", "Owner.Assigned", 1)
	call("POST", base, providerInvite, providerCookies, 201)
	call("GET", "/api/v1/entities/2/employees", "", providerCookies, 404)
	hybrid := (&app{db: db, host: "example.com", providerEmployee: true, adminRecovery: true, enableMCP: true}).routes()
	check(serve(hybrid, "GET", "/api/v1/auth/me", "", providerCookies), 200)
	check(serve(hybrid, "GET", base, "", providerCookies), 200)
	check(serve(hybrid, "GET", base, "", owner), 401)
	check(serve(hybrid, "GET", "/api/v1/auth/me", "", owner), 401)
	check(serve(hybrid, "POST", "/api/v1/auth/login", "{}", fresh()), 403)
	check(serve(hybrid, "GET", "/admin/", "", providerCookies), 302)
	check(serve(hybrid, "GET", "/admin/", "", owner), 302)
	opts := serve(hybrid, "GET", "/api/v1/auth/providers", "", nil)
	if !strings.Contains(opts.Body.String(), `"local_password_enabled":false`) ||
		!strings.Contains(opts.Body.String(), `"employee_password_enabled":true`) {
		t.Fatal("wrong hybrid sign-in options")
	}
	check(serve(hybrid, "POST", base, strings.Replace(good, "Caja.Employee", "Hybrid.Assigned", 1), providerCookies), 201)
	mcpAccess := func(h http.Handler, credential *httptest.ResponseRecorder, status int) {
		t.Helper()
		var key struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(credential.Body.Bytes(), &key); err != nil || len(key.Token) != 43 {
			t.Fatal("invalid synthetic credential")
		}
		r := httptest.NewRequest("GET", "http://example.com/api/v1/mcp/me", nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+key.Token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		check(out, status)
		if status != 200 && strings.Contains(out.Body.String(), "fixture-manager") {
			t.Fatal("denied credential leaked its account")
		}
	}
	const scopeJSON = `{"entity_id":1,"outlet_id":1}`
	legacyRead := call("POST", "/api/v1/auth/mcp-token", scopeJSON, owner, 201)
	mcpAccess(enabled, legacyRead, 200)
	mcpAccess(hybrid, legacyRead, 401)
	providerRead := serve(hybrid, "POST", "/api/v1/auth/mcp-token", scopeJSON, providerCookies)
	check(providerRead, 201)
	mcpAccess(hybrid, providerRead, 200)
	providerOnly := (&app{db: db, host: "example.com", providerOnly: true, employeeOnboarding: true}).routes()
	check(serve(providerOnly, "GET", "/api/v1/auth/me", "", providerCookies), 200)
	check(serve(providerOnly, "GET", base, "", providerCookies), 404)
	check(serve(providerOnly, "POST", employeeAuthBase+"/login", "{}", fresh()), 404)
	const login = employeeAuthBase + "/login"
	const change = employeeAuthBase + "/password"
	makeLogin := func(org int64, password string) string {
		b, _ := json.Marshal(map[string]any{"organization": org, "username": first.Username, "password": password})
		return string(b)
	}
	cs := fresh()
	call("POST", login, makeLogin(2, first.TemporaryPassword), cs, 401)
	a2 := fresh()
	var wg sync.WaitGroup
	answers := make(chan *httptest.ResponseRecorder, 2)
	for _, cookies := range [][]*http.Cookie{cs, a2} {
		wg.Go(func() { answers <- serve(enabled, "POST", login, makeLogin(1, first.TemporaryPassword), cookies) })
	}
	wg.Wait()
	close(answers)
	var changelog []*http.Cookie
	wins, denials := 0, 0
	for out := range answers {
		switch out.Code {
		case 200:
			wins++
			for _, k := range out.Result().Cookies() {
				if k.Name == "sessionid" {
					t.Fatal("a temporary credential issued a staff cookie")
				}
				if k.Name == employeeChangeCookie {
					if !k.HttpOnly || k.SameSite != http.SameSiteStrictMode || k.Path != change || k.MaxAge != 600 {
						t.Fatal("wrong restricted cookie")
					}
					changelog = append(cs, k)
				}
			}
		case 401:
			denials++
		default:
			t.Fatalf("unexpected first-use status %d", out.Code)
		}
	}
	if wins != 1 || denials != 1 || len(changelog) != len(cs)+1 {
		t.Fatal("temporary password was not consumed exactly once")
	}
	call("GET", "/api/v1/auth/me", "", changelog, 401)
	call("GET", "/api/v1/entities", "", changelog, 401)
	call("POST", "/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, changelog, 401)
	call("POST", change, `{"password":"short"}`, changelog, 422)
	call("POST", change, fmt.Sprintf(`{"password":%q}`, first.TemporaryPassword), changelog, 422)
	const newPass = "A fixture-only long passphrase"
	changeBody := `{"password":"` + newPass + `"}`
	done := call("POST", change, changeBody, changelog, 200)
	for _, k := range done.Result().Cookies() {
		if k.Name == "sessionid" {
			t.Fatal("replacement silently issued staff session")
		}
	}
	call("POST", change, changeBody, changelog, 401)
	call("POST", login, makeLogin(1, first.TemporaryPassword), cs, 401)
	auth := call("POST", login, makeLogin(1, newPass), cs, 200).Result().Cookies()
	call("GET", "/api/v1/auth/me", "", auth, 200)
	call("GET", "/api/v1/entities/2/outlets", "", auth, 404)
	call("GET", "/api/v1/entities/1/outlets", "", auth, 200)
	posCatalog := "/api/v1/entities/1/outlets/1/pos/catalog"
	flags := call("GET", posCatalog, "", auth, 200).Body.String()
	if strings.Contains(flags, `"cuentas_mesa"`) || strings.Contains(flags, `"registro_medios_pago"`) {
		t.Fatalf("managed employee received withheld table write flags: %s", flags)
	}
	flags = call("GET", posCatalog, "", owner, 200).Body.String()
	if !strings.Contains(flags, `"cuentas_mesa":true`) || !strings.Contains(flags, `"registro_medios_pago":true`) {
		t.Fatalf("authorized ordinary clerk lost POS actions: %s", flags)
	}
	tenderPath := "/api/v1/entities/1/outlets/1/pos/table-checks/1/tender"
	call("GET", tenderPath, "", auth, 403)
	call("POST", tenderPath, "{}", append(auth, cs...), 403)
	call("POST", "/api/v1/entities/1/outlets/1/pos/table-checks", "{}", append(auth, cs...), 403)
	call("GET", base, "", auth, 403)
	check(serve(providerOnly, "GET", "/api/v1/auth/me", "", auth), 401)
	hybridAuth := serve(hybrid, "POST", login, makeLogin(1, newPass), fresh())
	check(hybridAuth, 200)
	hybridCookies := hybridAuth.Result().Cookies()
	check(serve(hybrid, "GET", "/api/v1/auth/me", "", hybridCookies), 200)
	check(serve(hybrid, "GET", "/api/v1/entities/2/outlets", "", hybridCookies), 404)
	check(serve(hybrid, "GET", "/admin/", "", hybridCookies), 302)
	check(serve(hybrid, "POST", "/api/v1/auth/mcp-token", scopeJSON, hybridCookies), 403)
	check(serve(hybrid, "POST", "/api/v1/entities/1/suppliers", `{"razon_social":"Hybrid fixture supplier","cuit":"00000000027"}`, hybridCookies), 201)
	check(serve((&app{db: db, host: "example.com"}).routes(), "GET", "/api/v1/auth/me", "", auth), 401)
	call("POST", "/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, auth, 403)
	call("POST", "/api/v1/entities/1/suppliers", `{"razon_social":"Fixture baseline supplier","cuit":"00000000000"}`, auth, 201)
	var internal, password string
	if err := db.QueryRowContext(ctx, "SELECT username,password FROM users WHERE id=$1", first.ID).Scan(&internal, &password); err != nil {
		t.Fatal(err)
	}
	// Reject existing stale-format MCP rows even if their scoped references
	// and current user digest coincide with a managed employee.
	var loginCookie string
	for _, k := range auth {
		if k.Name == "sessionid" {
			loginCookie = k.Value
		}
	}
	if loginCookie == "" {
		t.Fatal("missing employee session")
	}
	loginHash := sha256.Sum256([]byte(loginCookie))
	credentialHash := sha256.Sum256([]byte(password))
	namedHash := sha256.Sum256([]byte("synthetic named row"))
	legacyHash := sha256.Sum256([]byte("synthetic legacy row"))
	now := time.Now().Unix()
	var namedID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO mcp_proposal_sessions(actor_id,entity_id,outlet_id,login_hash,bearer_hash,password_digest,issued_at,expires_at)
  VALUES($1,1,1,$2,$3,$4,$5,CAST($5 AS BIGINT)+120) RETURNING id`, first.ID, loginHash[:], namedHash[:], credentialHash[:], now).Scan(&namedID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO mcp_proposal_session_actions(session_id,action) VALUES($1,'pos_product_rename')`, namedID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO mcp_wiki_proposal_tokens(actor_id,entity_id,outlet_id,token_hash,password_digest,issued_at,expires_at,purpose)
  VALUES($1,1,1,$2,$3,$4,CAST($4 AS BIGINT)+120,$5)`, first.ID, legacyHash[:], credentialHash[:], now, wikiKeyPurpose); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	probe := &app{db: db}
	scope := wikiProposalScope{entity: 1, outlet: 1}
	_, _, namedErr := probe.loadNamedProposalSession(ctx, tx, namedHash[:], &scope, posRenameSessionAction)
	_, _, legacyErr := probe.loadProposalKey(ctx, tx, legacyHash[:], &scope, wikiKeyPurpose)
	tx.Rollback()
	if namedErr != unauthenticated || legacyErr != unauthenticated {
		t.Fatal("managed employee bearer row accepted")
	}

	if verifyPassword(password, newPass) {
		t.Fatal("employee password escaped to platform login")
	}
	call("POST", "/api/v1/auth/login", fmt.Sprintf(`{"usuario":%q,"clave":%q}`, internal, newPass), fresh(), 401)
	call("POST", "/api/v1/entities/2/memberships", fmt.Sprintf(`{"usuario":%q,"rol":"read_only"}`, internal), other, 404)
	route := fmt.Sprintf("%s/%d", base, first.ID)
	call("POST", fmt.Sprintf("/api/v1/entities/2/employees/%d/reset", first.ID), "{}", other, 404)
	// Dispatch the native handler only after the usual outer auth check.
	// Complete the real owner action before the native domain transaction.
	queuedWrite := func(method, pattern, target, body string, cookie []*http.Cookie,
		next func(http.ResponseWriter, *http.Request, user) error, revoke func()) {
		t.Helper()
		guardApp := &app{db: db, host: "example.com", employeeOnboarding: true}
		allowed := make(chan struct{})
		resume := make(chan struct{})
		defer close(resume)
		settle := make(chan struct{})
		native := http.NewServeMux()
		native.HandleFunc(pattern, guardApp.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
			close(allowed)
			select {
			case <-resume:
				return errors.New("test stopped")
			case <-settle:
			}
			return next(w, r, u)
		}))
		triggered := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = r.WithContext(context.WithValue(r.Context(), employeeLoginContextKey{}, true))
				native.ServeHTTP(w, r)
			})
			triggered <- serve(handler, method, target, body, cookie)
		}()
		select {
		case <-allowed:
		case <-ctx.Done():
			t.Fatal("native employee request never reached the transaction boundary")
		}
		revoke()
		close(settle)
		select {
		case result := <-triggered:
			check(result, 401)
		case <-ctx.Done():
			t.Fatal("native employee request did not settle")
		}
	}
	guardApp := &app{db: db, host: "example.com", employeeOnboarding: true}
	var reset employeeInviteReply
	queuedWrite("POST", "/api/v1/entities/{entity}/suppliers", "/api/v1/entities/1/suppliers",
		`{"razon_social":"Fixture queued supplier","cuit":"00000000019"}`, auth, guardApp.createSupplier,
		func() { reset = decodeInvite(call("POST", route+"/reset", "{}", owner, 200)) })

	var written int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM suppliers WHERE cuit='00000000019'").Scan(&written); err != nil || written != 0 {
		t.Fatal("revoked native employee request changed supplier data")
	}
	call("GET", "/api/v1/auth/me", "", auth, 401)
	call("POST", login, makeLogin(1, newPass), fresh(), 401)
	if _, err := db.ExecContext(ctx, `UPDATE organization_employees SET temporary_expires_at=1 WHERE user_id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	call("POST", login, makeLogin(1, reset.TemporaryPassword), fresh(), 401)
	reset = decodeInvite(call("POST", route+"/reset", "{}", owner, 200))
	cs = fresh()
	resetCookie := call("POST", login, makeLogin(1, reset.TemporaryPassword), cs, 200).Result().Cookies()
	if _, err := db.ExecContext(ctx, `UPDATE organization_employees SET change_expires_at=1 WHERE user_id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	call("POST", change, changeBody, append(cs, resetCookie...), 401)
	call("POST", route+"/disable", "{}", owner, 200)
	call("POST", route+"/reset", "{}", owner, 409)
	call("POST", login, makeLogin(1, reset.TemporaryPassword), fresh(), 401)
	var status string
	var stored int
	if err := db.QueryRowContext(ctx, `SELECT status,(CASE WHEN temporary_hash IS NULL AND change_hash IS NULL AND password_hash IS NULL THEN 0 ELSE 1 END) FROM organization_employees WHERE user_id=$1`, first.ID).Scan(&status, &stored); err != nil || status != "disabled" || stored != 0 {
		t.Fatal("disabled employee retained a credential")
	}
	// Independently activate the same handle at the other organization.
	// Losing access there must also stop an admitted native edit.
	otherCS := fresh()
	otherRestricted := call("POST", login, makeLogin(2, second.TemporaryPassword), otherCS, 200).Result().Cookies()
	call("POST", change, changeBody, append(otherCS, otherRestricted...), 200)
	otherAuth := call("POST", login, makeLogin(2, newPass), otherCS, 200).Result().Cookies()
	call("POST", "/api/v1/entities/2/suppliers", `{"razon_social":"Fixture original","cuit":"00000000019"}`, otherAuth, 201)
	var supplier int64
	if err := db.QueryRowContext(ctx, "SELECT id FROM suppliers WHERE entity_id=2 AND cuit='00000000019'").Scan(&supplier); err != nil {
		t.Fatal(err)
	}
	queuedWrite("PUT", "/api/v1/entities/{entity}/suppliers/{supplier}", fmt.Sprintf("/api/v1/entities/2/suppliers/%d", supplier),
		`{"razon_social":"Fixture queued edit","cuit":"00000000019"}`, otherAuth, guardApp.updateSupplier,
		func() {
			call("POST", fmt.Sprintf("/api/v1/entities/2/employees/%d/disable", second.ID), "{}", other, 200)
		})
	var current string
	if err := db.QueryRowContext(ctx, "SELECT name FROM suppliers WHERE id=$1", supplier).Scan(&current); err != nil || current != "Fixture original" {
		t.Fatal("disabled native employee request modified supplier")
	}
	call("GET", "/api/v1/auth/me", "", otherAuth, 401)

}
