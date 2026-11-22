package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

type providerFakeRoundTrip func(*http.Request) (*http.Response, error)

func (f providerFakeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderEndpointPins(t *testing.T) {
	consumer, err := pinnedProviderEndpoints("microsoft", "")
	if err != nil || consumer.Tenant != microsoftConsumerTenant || !strings.Contains(consumer.Auth, "/consumers/") || !consumer.PKCE {
		t.Fatal(consumer, err)
	}
	for _, tenant := range []string{"common", "organizations", "consumers", "example.org", "", microsoftConsumerTenant} {
		if _, err := pinnedProviderEndpoints("microsoft-work", tenant); err == nil {
			t.Fatalf("accepted %s", tenant)
		}
	}
	apple, err := pinnedProviderEndpoints("apple", "")
	if err != nil || !apple.FormPost || apple.PKCE {
		t.Fatal(apple, err)
	}
	for _, origin := range []string{"http://example.com", "https://example.com/path", "https://example.com?", "https://someone@example.com", "https://example.com#id"} {
		if _, err := newHostedProviderAuth(origin, map[string]providerClientSettings{"google": {"client", "secret", ""}}, nil); err == nil {
			t.Fatalf("accepted %s", origin)
		}
	}
}

func TestMicrosoftPublishedKeysStayInIssuerScope(t *testing.T) {
	e, err := pinnedProviderEndpoints("microsoft", "")
	if err != nil {
		t.Fatal(err)
	}
	allowed := fmt.Sprintf(`{"keys":[{"kid":"exact","issuer":%q},{"kid":"template","issuer":"https://login.microsoftonline.com/{tenantid}/v2.0"},{"kid":"other","issuer":"https://login.microsoftonline.com/00000000-0000-0000-0000-000000000001/v2.0"},{"kid":"unscoped"}]}`, e.Issuer)
	base := providerFakeRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(allowed))}, nil
	})
	pin := providerOutbound{token: e.Token, jwks: e.JWKS, microsoftIssuer: e.Issuer, base: base}
	r := httptest.NewRequest(http.MethodGet, e.JWKS, nil)
	reply, err := pin.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer reply.Body.Close()
	data, err := io.ReadAll(reply.Body)
	if err != nil || !strings.Contains(string(data), "exact") || !strings.Contains(string(data), "template") ||
		strings.Contains(string(data), "unscoped") || strings.Contains(string(data), "other") {
		t.Fatal("Microsoft key filter returned the wrong scope", err)
	}
	if _, err := pin.RoundTrip(httptest.NewRequest(http.MethodGet, "https://foreign.invalid/keys", nil)); err == nil {
		t.Fatal("accepted an unpinned target")
	}
}

func TestPostgresProviderLoginSynthetic(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(username,password,active,staff,superuser)
		VALUES('same-email@example.invalid','test-legacy-only',TRUE,TRUE,TRUE)`); err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key := jose.JSONWebKey{Key: rsaKey, KeyID: "synthetic-signing", Algorithm: string(jose.RS256), Use: "sig"}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	type registeredCode struct {
		settings      providerEndpoints
		authorization url.Values
		change        func(map[string]any)
	}
	codes := map[string]registeredCode{}
	tokenRequests := 0
	var mu sync.Mutex
	const origin = "https://example.com"
	settings := map[string]providerClientSettings{
		"google":         {"synthetic-client", "synthetic-secret", ""},
		"microsoft":      {"synthetic-client", "synthetic-secret", ""},
		"microsoft-work": {"synthetic-client", "synthetic-secret", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
	}
	endpoints := map[string]providerEndpoints{}
	for flow, config := range settings {
		e, err := pinnedProviderEndpoints(flow, config.Tenant)
		if err != nil {
			t.Fatal(err)
		}
		endpoints[e.JWKS] = e
	}
	fake := providerFakeRoundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		out := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}}
		if r.Method == "GET" {
			e, ok := endpoints[r.URL.String()]
			if !ok {
				t.Errorf("unexpected key endpoint %s", r.URL)
				return nil, errProviderIdentity
			}
			published, err := json.Marshal(key.Public())
			if err != nil {
				return nil, err
			}
			var jwk map[string]any
			if err = json.Unmarshal(published, &jwk); err != nil {
				return nil, err
			}
			if e.Tenant != "" {
				jwk["issuer"] = "https://login.microsoftonline.com/{tenantid}/v2.0"
			}
			raw, err := json.Marshal(map[string]any{"keys": []any{jwk}})
			if err != nil {
				return nil, err
			}
			out.Body = io.NopCloser(strings.NewReader(string(raw)))
			return out, nil
		}
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		tokenRequests++
		code, ok := codes[r.Form.Get("code")]
		secret := "synthetic-secret"
		if !ok || r.URL.String() != code.settings.Token || r.Form.Get("client_secret") != secret ||
			r.Form.Get("redirect_uri") != code.authorization.Get("redirect_uri") || r.Form.Get("grant_type") != "authorization_code" {
			t.Error("unexpected token exchange")
			return nil, errProviderIdentity
		}
		if code.settings.PKCE {
			v := r.Form.Get("code_verifier")
			challenge := sha256.Sum256([]byte(v))
			if !providerRandom(v) || base64.RawURLEncoding.EncodeToString(challenge[:]) != code.authorization.Get("code_challenge") {
				t.Error("wrong PKCE verifier")
				return nil, errProviderIdentity
			}
		} else if r.Form.Has("code_verifier") {
			t.Error("unadvertised Apple PKCE field")
			return nil, errProviderIdentity
		}
		claims := map[string]any{"iss": code.settings.Issuer, "sub": "synthetic-same-subject", "aud": "synthetic-client",
			"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": code.authorization.Get("nonce"),
			"email": "same-email@example.invalid", "email_verified": true, "roles": []string{"admin"}, "groups": []string{"owner"}}
		if code.settings.Tenant != "" {
			claims["tid"] = code.settings.Tenant
		}
		if code.change != nil {
			code.change(claims)
		}
		raw, _ := json.Marshal(claims)
		signed, err := signer.Sign(raw)
		if err != nil {
			return nil, err
		}
		jwt, err := signed.CompactSerialize()
		if err != nil {
			return nil, err
		}
		response, _ := json.Marshal(map[string]any{"access_token": "never-store-token", "refresh_token": "never-store-refresh", "id_token": jwt, "token_type": "Bearer"})
		out.Body = io.NopCloser(strings.NewReader(string(response)))
		return out, nil
	})
	env := map[string]string{"GOMAR_AUTH_PUBLIC_ORIGIN": origin, "GOMAR_AUTH_PROVIDERS": "google,microsoft,microsoft-work"}
	secrets := make(map[string]string)
	for id, v := range settings {
		prefix := "GOMAR_AUTH_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
		path := "/synthetic/provider-test/" + id
		env[prefix+"_CLIENT_ID"] = v.ClientID
		env[prefix+"_CLIENT_SECRET_FILE"] = path
		env[prefix+"_TENANT_ID"] = v.Tenant
		secrets[path] = v.ClientSecret
	}
	config, err := providerRuntimeAuth(providerRuntimeSettings{
		enabled: true, protectedIngress: true, authority: "example.com",
		migrations: []string{providerAuthMigration}, network: fake,
		env:        func(key string) string { return env[key] },
		readSecret: func(path string) (string, error) { return secrets[path], nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com", providerAuth: config}
	router := a.routes()
	disabled := (&app{db: db, host: "example.com"}).routes()
	request := func(h http.Handler, method, path, body, source string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body)).WithContext(ctx)
		if source != "" {
			r.Header.Set("Origin", source)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got %d, want %d: %s", w.Code, status, w.Body.String())
		}
	}
	// Even a constructed prototype adapter cannot become a wired web provider.
	prototype, err := newOIDCProvider(origin, "apple", providerClientSettings{ClientID: "synthetic-client", ClientSecret: "synthetic-secret"}, fake)
	if err != nil {
		t.Fatal(err)
	}
	config.providers["apple"] = prototype
	withheld := a.routes()
	delete(config.providers, "apple")
	expect(request(withheld, "POST", "/api/v1/auth/providers/apple/start", "", origin, nil), 404)
	expect(request(withheld, "POST", "/api/v1/auth/providers/apple/callback", "", origin, nil), 404)
	config.providers["apple"] = prototype
	advertised := request(withheld, "GET", "/api/v1/auth/providers", "", "", nil)
	delete(config.providers, "apple")
	expect(advertised, 200)
	if strings.Contains(advertised.Body.String(), `"apple"`) {
		t.Fatal("disabled provider was advertised")
	}
	csrf := request(router, "GET", "/api/v1/auth/csrf", "", "", nil).Result().Cookies()
	const prefix = "/api/v1/auth/providers/"
	expect(request(disabled, "POST", prefix+"google/start", "", origin, csrf), 404)
	w := request(disabled, "GET", "/api/v1/auth/providers", "", "", nil)
	expect(w, 200)
	if !strings.Contains(w.Body.String(), `"providers":[]`) || !strings.Contains(w.Body.String(), `"local_password_enabled":true`) {
		t.Fatal(w.Body.String())
	}
	expect(request(router, "POST", prefix+"google/start", "", "https://foreign.invalid", csrf), 403)
	expect(request(router, "POST", prefix+"google/start", "", origin, nil), 403)
	type attempt struct {
		query  url.Values
		cookie *http.Cookie
	}
	start := func(flow string) attempt {
		t.Helper()
		w := request(router, "POST", prefix+flow+"/start", "", origin, csrf)
		expect(w, 200)
		var wire struct {
			URL string `json:"authorization_url"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(wire.URL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		e := config.providers[flow].endpoints
		if u.Scheme+"://"+u.Host+u.Path != e.Auth || q.Get("response_type") != "code" || (!e.FormPost && q.Get("scope") != "openid") || (e.FormPost && q.Has("scope")) ||
			!providerRandom(q.Get("state")) || !providerRandom(q.Get("nonce")) {
			t.Fatal(wire)
		}
		if e.PKCE && q.Get("code_challenge_method") != "S256" || e.FormPost && (q.Get("response_mode") != "form_post" || q.Has("code_challenge")) {
			t.Fatal("wrong protocol projection")
		}
		cookies := w.Result().Cookies()
		mode := http.SameSiteLaxMode
		if e.FormPost {
			mode = http.SameSiteNoneMode
		}
		if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != mode || cookies[0].Path != "/" ||
			cookies[0].Name != providerCookiePrefix+q.Get("state") {
			t.Fatal(cookies)
		}
		return attempt{q, cookies[0]}
	}
	callback := func(flow, code string, v attempt, cookie *http.Cookie, change func(map[string]any)) *httptest.ResponseRecorder {
		t.Helper()
		e := config.providers[flow].endpoints
		mu.Lock()
		codes[code] = registeredCode{e, v.query, change}
		mu.Unlock()
		form := url.Values{"code": {code}, "state": {v.query.Get("state")}, "user": {`{"email":"never-use-browser-content@example.invalid"}`}}
		var cookies []*http.Cookie
		if cookie != nil {
			cookies = []*http.Cookie{cookie}
		}
		if e.FormPost {
			return request(router, "POST", prefix+flow+"/callback", form.Encode(), "https://appleid.apple.com", cookies)
		}
		return request(router, "GET", prefix+flow+"/callback?"+form.Encode(), "", "", cookies)
	}
	g := start("google")
	other := start("google")
	expect(callback("google", "bad-cookie", g, other.cookie, nil), 401)
	expect(callback("google", "missing-cookie", g, nil, nil), 401)
	expect(callback("microsoft", "wrong-provider", g, g.cookie, nil), 401)
	expired := start("google")
	hash := sha256.Sum256([]byte(expired.query.Get("state")))
	if _, err := db.ExecContext(ctx, `UPDATE provider_auth_flows SET created_at=$1,expires_at=$2 WHERE state_hash=$3`,
		time.Now().Add(-10*time.Minute).Unix(), time.Now().Add(-5*time.Minute).Unix(), hash[:]); err != nil {
		t.Fatal(err)
	}
	expect(callback("google", "expired-state", expired, expired.cookie, nil), 401)
	if tokenRequests != 0 {
		t.Fatal("invalid state reached provider transport")
	}
	var sessions []*http.Cookie
	for _, flow := range []string{"google", "microsoft", "microsoft-work"} {
		v := start(flow)
		expected := callback(flow, "valid-"+flow, v, v.cookie, nil)
		expect(expected, 303)
		if expected.Header().Get("Location") != "/" || expected.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal(expected.Header())
		}
		for _, c := range expected.Result().Cookies() {
			if c.Name == "sessionid" {
				sessions = append(sessions, c)
			}
		}
		expect(callback(flow, "replay-"+flow, v, v.cookie, nil), 401)
	}
	if len(sessions) != 3 {
		t.Fatal("expected three ordinary sessions")
	}
	// An email collision never binds an existing administrator, and the
	// same subject in different issuer realms is independent.
	var identities, members, admins int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM provider_identities),(SELECT count(*) FROM memberships),
		(SELECT count(*) FROM users WHERE superuser)`).Scan(&identities, &members, &admins); err != nil || identities != 3 || members != 0 || admins != 1 {
		t.Fatal(identities, members, admins, err)
	}
	verify := httptest.NewRequest("GET", origin+"/api/v1/auth/me", nil).WithContext(ctx)
	verify.AddCookie(sessions[0])
	userRecord, err := currentUser(nil, verify, db)
	if err != nil || userRecord.staff || userRecord.superuser || userRecord.password != providerOnlyPassword {
		t.Fatal("provider session not ordinary", err)
	}
	existing := start("google")
	expect(callback("google", "existing-identity", existing, existing.cookie, nil), 303)
	for i, change := range []func(map[string]any){
		func(m map[string]any) { m["iss"] = "https://foreign.invalid" },
		func(m map[string]any) { m["aud"] = "foreign-client" },
		func(m map[string]any) { m["nonce"] = "wrong-nonce" },
		func(m map[string]any) { m["exp"] = time.Now().Add(-time.Minute).Unix() },
		func(m map[string]any) { m["tid"] = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" },
		func(m map[string]any) { m["azp"] = "other-client" },
	} {
		v := start("microsoft")
		expect(callback("microsoft", "bad-claim-"+string(rune('a'+i)), v, v.cookie, change), 401)
	}
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM provider_identities").Scan(&identities); err != nil || identities != 3 {
		t.Fatal(identities, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE users SET active=FALSE WHERE id=$1", userRecord.id); err != nil {
		t.Fatal(err)
	}
	inactive := start("google")
	expect(callback("google", "disabled-user", inactive, inactive.cookie, nil), 401)
}
