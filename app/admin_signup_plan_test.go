package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

type protectedSignupPlanTest struct {
	t      *testing.T
	ctx    context.Context
	app    *app
	server *httptest.Server
}

func newProtectedSignupPlanTest(t *testing.T, ctx context.Context, db *sql.DB) protectedSignupPlanTest {
	t.Helper()
	var routes http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This dedicated synthetic listener is entirely in-process. No
		// header, TLS bit, or remote address can set this context in the app.
		routes.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), privateHTTPSKey{}, true)))
	}))
	t.Cleanup(server.Close)
	a := &app{host: server.Listener.Addr().String(), db: db, adminRecovery: true,
		signupPlanAdmin: &adminSignupPlanFixture{audience: signupFixtureTarget("legacy").Audience}}
	routes = a.routes()
	server.StartTLS()
	return protectedSignupPlanTest{t, ctx, a, server}
}

func (p protectedSignupPlanTest) request(c *http.Client, path string, form url.Values, want int) string {
	p.t.Helper()
	method := http.MethodGet
	var body string
	if form != nil {
		method = http.MethodPost
		body = form.Encode()
	}
	req, err := http.NewRequestWithContext(p.ctx, method, p.server.URL+path, strings.NewReader(body))
	if err != nil {
		p.t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", p.server.URL)
	}
	resp, err := c.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 100<<10))
	if err != nil || resp.StatusCode != want {
		p.t.Fatalf("%s protected operator request status=%d; want %d; %v", method, resp.StatusCode, want, err)
	}
	if path == adminSignupPlanPath && (resp.Header.Get("Cache-Control") != "private, no-store" ||
		resp.Header.Get("Referrer-Policy") != "same-origin" || resp.Header.Get("Location") != "") {
		p.t.Fatal("private screen response lost guard headers")
	}
	return string(raw)
}

func (p protectedSignupPlanTest) login() *http.Client {
	p.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		p.t.Fatal(err)
	}
	c := &http.Client{Jar: jar, Transport: p.server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	p.request(c, "/admin/login/", nil, 200)
	p.request(c, "/admin/login/", url.Values{"username": {"same-email@example.invalid"},
		"password": {"synthetic-recovery-password"}, "csrfmiddlewaretoken": {p.cookie(c, adminRecoveryCSRF)}}, 302)
	if p.cookie(c, adminRecoveryCookie) == "" {
		p.t.Fatal("fixture login did not issue a recovery realm cookie")
	}
	return c
}

func (p protectedSignupPlanTest) cookie(c *http.Client, name string) string {
	base, _ := url.Parse(p.server.URL + adminSignupPlanPath)
	for _, cookie := range c.Jar.Cookies(base) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (p protectedSignupPlanTest) nativeRequest(c *http.Client, path string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, p.server.URL+path, nil).
		WithContext(context.WithValue(p.ctx, privateHTTPSKey{}, true))
	req.Header.Set("Origin", p.server.URL)
	for _, name := range []string{adminRecoveryCookie, adminRecoveryCSRF} {
		req.AddCookie(&http.Cookie{Name: name, Value: p.cookie(c, name)})
	}
	req.Header.Set("X-CSRFToken", p.cookie(c, adminRecoveryCSRF))
	return req
}

func TestPostgresNativeSignupPlanAdmin(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	f := signupFixture(t, ctx, db)
	browserCase := signupFixtureTarget("operator-screen")
	f.designateAndApprove("other", browserCase)
	revoked := signupFixtureTarget("operator-revoked")
	revokeRef := f.designateAndApprove("desk", revoked)
	expired := signupFixtureTarget("operator-expired")
	f.designateAndApprove("mobile", expired)
	waitingScope := signupFixtureTarget("operator-waiting")
	f.designateAndApprove("desk", waitingScope)
	superseded := signupFixtureTarget("operator-superseded")
	f.designateAndApprove("desk", superseded)
	next := superseded
	next.Revision++
	next.RequestRef += "-next"
	f.designateAndApprove("desk", next)
	consumed := signupFixtureTarget("operator-consumed")
	f.designateAndApprove("mobile", consumed)
	foreign := signupFixtureTarget("operator-foreign")
	foreign.Audience = "another-hosted-audience"
	f.designateAndApprove("desk", foreign)
	approvalExpired := signupFixtureTarget("operator-expired-approval")
	if err := f.ops.designateNativeOwner(f.recovery("/admin/__signup-fixture-designate"), approvalExpired, f.actors["desk"]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO gomar.native_owner_approvals
		SELECT $1,$2,$3,operator_id,NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute'
		FROM gomar.native_owner_approvals WHERE approval_ref=$4`,
		approvalExpired.Intent, approvalExpired.Revision, "approval-"+newProviderRandom(), revokeRef); err != nil {
		t.Fatal(err)
	}
	unapproved := signupFixtureTarget("operator-unapproved")
	if err := f.ops.designateNativeOwner(f.recovery("/admin/__signup-fixture-designate"), unapproved, f.actors["desk"]); err != nil {
		t.Fatal(err)
	}
	p := newProtectedSignupPlanTest(t, ctx, db)
	operator, second := p.login(), p.login()
	snapshot := func() (n [7]int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.native_signup_plans),
			(SELECT count(*) FROM gomar.native_owner_receipts),(SELECT count(*) FROM gomar.native_owner_install_drafts),
			(SELECT count(*) FROM gomar.native_owner_pending_installs),(SELECT count(*) FROM gomar.entities),
			(SELECT count(*) FROM gomar.memberships),(SELECT count(*) FROM gomar.outlet_grants)`).
			Scan(&n[0], &n[1], &n[2], &n[3], &n[4], &n[5], &n[6]); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := snapshot()
	// Default denial even with a valid recovery login; TLS and forwarded
	// headers are not protected ingress outside the dedicated fixture.
	for _, tc := range []struct {
		app     *app
		private bool
	}{
		{&app{host: p.app.host, db: db, adminRecovery: true}, true},
		{p.app, false},
		{&app{host: p.app.host, db: db, signupPlanAdmin: p.app.signupPlanAdmin}, true},
	} {
		r := p.nativeRequest(operator, adminSignupPlanPath)
		r.Method, r.RemoteAddr = http.MethodGet, "127.0.0.1:12345"
		r.Header.Set("X-Forwarded-Proto", "https")
		r = r.WithContext(context.WithValue(ctx, privateHTTPSKey{}, tc.private))
		rec := httptest.NewRecorder()
		tc.app.routes().ServeHTTP(rec, r)
		if rec.Code != 404 || rec.Header().Get("Location") != "" {
			t.Fatal("default or spoofed private route was exposed")
		}
	}
	ordinary := f.client()
	if status, _, _ := f.request(ordinary, "GET", "/__signup-fixture-sign-in/legacy", "", ""); status != 204 {
		t.Fatal("fixture provider login failed")
	}
	r := f.owner("legacy", adminSignupPlanPath)
	r.Host, r.Method = p.app.host, http.MethodGet
	r = r.WithContext(context.WithValue(ctx, privateHTTPSKey{}, true))
	rec := httptest.NewRecorder()
	p.app.routes().ServeHTTP(rec, r)
	if rec.Code != 404 {
		t.Fatal("ordinary provider session tried the protected screen")
	}
	classic := postgresTestCookies(t, (&app{host: "example.com", db: db}).routes(), "same-email@example.invalid", "synthetic-recovery-password")
	r = httptest.NewRequest(http.MethodGet, p.server.URL+adminSignupPlanPath, nil).
		WithContext(context.WithValue(ctx, privateHTTPSKey{}, true))
	for _, c := range classic {
		r.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	p.app.routes().ServeHTTP(rec, r)
	if rec.Code != 404 {
		t.Fatal("ordinary classic superuser session tried the protected screen")
	}
	replay := p.nativeRequest(operator, adminSignupPlanPath)
	replay.Method = http.MethodGet
	replay.Header.Set("Authorization", "Bearer fixture")
	rec = httptest.NewRecorder()
	p.app.routes().ServeHTTP(rec, replay)
	if rec.Code != 404 {
		t.Fatal("bearer header reached the protected screen")
	}
	p.request(operator, adminSignupPlanPath, nil, 200)
	csrf := func(c *http.Client, action string) url.Values {
		return url.Values{"action": {action}, "csrfmiddlewaretoken": {p.cookie(c, adminRecoveryCSRF)}}
	}
	token := regexp.MustCompile(`name="ticket" value="([A-Za-z0-9_-]{43})"`)
	post := func(c *http.Client, form url.Values, want int) string {
		t.Helper()
		raw := p.request(c, adminSignupPlanPath, form, want)
		for _, value := range []string{"private-request", "private-business", "same-email@example.invalid"} {
			if strings.Contains(raw, value) {
				t.Fatal("protected output exposed a reference or actor")
			}
		}
		return raw
	}
	open := func(c *http.Client, ref string, want int) string {
		t.Helper()
		data := csrf(c, "open")
		data.Set("case", ref)
		raw := post(c, data, want)
		if want != 200 {
			return ""
		}
		parts := token.FindStringSubmatch(raw)
		if len(parts) != 2 || !strings.Contains(raw, "Nombre visible del local outlet-a") ||
			!strings.Contains(raw, "Nombre visible del local outlet-b") {
			t.Fatal("no exact server-fixed branch slots")
		}
		return parts[1]
	}
	review := func(c *http.Client, first string, want int) string {
		t.Helper()
		data := csrf(c, "review")
		data.Set("ticket", first)
		data.Set("organization", "Marca  de prueba")
		data.Set("branch_0", "Centro <seguro>")
		data.Set("branch_1", "Norte  dos")
		raw := post(c, data, want)
		if want != 200 {
			return ""
		}
		parts := token.FindStringSubmatch(raw)
		if len(parts) != 2 || parts[1] == first || !strings.Contains(raw, "Centro &lt;seguro&gt;") ||
			!strings.Contains(raw, "Norte  dos") || strings.Contains(raw, `name="branch_0"`) {
			t.Fatal("review did not freeze and escape the displayed labels")
		}
		return parts[1]
	}
	save := func(c *http.Client, key string) url.Values {
		form := csrf(c, "save")
		form.Set("ticket", key)
		form.Set("confirm", "yes")
		return form
	}

	bad := csrf(operator, "open")
	bad.Set("case", browserCase.RequestRef)
	bad.Add("case", consumed.RequestRef)
	post(operator, bad, 422)
	bad = csrf(operator, "open")
	bad.Set("case", browserCase.RequestRef)
	bad.Set("csrfmiddlewaretoken", "invalid")
	post(operator, bad, 403)
	csrfCorrect := csrf(operator, "open")
	csrfCorrect.Set("case", browserCase.RequestRef)
	for _, origin := range []string{"https://another-origin.invalid", "null", ""} {
		r = httptest.NewRequest(http.MethodPost, p.server.URL+adminSignupPlanPath, strings.NewReader(csrfCorrect.Encode())).
			WithContext(context.WithValue(ctx, privateHTTPSKey{}, true))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Cookie", p.nativeRequest(operator, adminSignupPlanPath).Header.Get("Cookie"))
		rec = httptest.NewRecorder()
		p.app.routes().ServeHTTP(rec, r)
		if rec.Code != 403 {
			t.Fatal("unverified Origin reached the protected case")
		}
	}
	p.request(operator, adminSignupPlanPath+"?case=untrusted", nil, 404)
	for _, denied := range []string{"absent", foreign.RequestRef, unapproved.RequestRef, superseded.RequestRef, approvalExpired.RequestRef} {
		open(operator, denied, 404)
	}
	first := open(operator, signupFixtureTarget("legacy").RequestRef, 200)
	review(second, first, 404)
	reviewed := review(operator, first, 200)
	post(second, save(second, reviewed), 404)
	for _, field := range []string{"organization", "branch_0", "intent", "case", "actor_id"} {
		bad = save(operator, reviewed)
		bad.Set(field, "swapped")
		post(operator, bad, 422)
	}
	post(operator, save(operator, first), 404)
	if snapshot() != before {
		t.Fatal("opening, review or denial wrote durable rows")
	}
	if raw := post(operator, save(operator, reviewed), 200); !strings.Contains(raw, "Plan guardado") {
		t.Fatal("protected save was not acknowledged")
	}
	post(operator, save(operator, reviewed), 404)
	open(operator, signupFixtureTarget("legacy").RequestRef, 404)
	if after := snapshot(); after[0] != before[0]+1 || !slices.Equal(after[1:], before[1:]) {
		t.Fatal("protected save wrote more than one display plan")
	}

	revocationKey := review(operator, open(operator, revoked.RequestRef, 200), 200)
	if err := p.app.revokeNativeOwnerApproval(p.nativeRequest(operator, "/admin/__fixture-revoke"), revoked, revokeRef); err != nil {
		t.Fatal(err)
	}
	post(operator, save(operator, revocationKey), 404)
	open(operator, revoked.RequestRef, 404)

	expiredKey := review(operator, open(operator, expired.RequestRef, 200), 200)
	p.app.signupPlanAdmin.mu.Lock()
	ticket := p.app.signupPlanAdmin.tickets[expiredKey]
	ticket.expires = nativeOwnerNow().Add(-time.Second)
	p.app.signupPlanAdmin.tickets[expiredKey] = ticket
	p.app.signupPlanAdmin.mu.Unlock()
	post(operator, save(operator, expiredKey), 404)

	waitingKey := review(operator, open(operator, waitingScope.RequestRef, 200), 200)
	waitingForm := save(operator, waitingKey)
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var blockerPID int
	if err = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM gomar.users
		WHERE id=$1 FOR NO KEY UPDATE`, f.actors["desk"]).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	p.app.signupPlanAdmin.mu.Lock()
	ticket = p.app.signupPlanAdmin.tickets[waitingKey]
	cutoff := nativeOwnerNow().Add(1250 * time.Millisecond)
	ticket.expires = cutoff // A bounded test ticket; no change to PG or app clocks.
	p.app.signupPlanAdmin.tickets[waitingKey] = ticket
	p.app.signupPlanAdmin.mu.Unlock()
	beforeWait := snapshot()
	waitingRequest := httptest.NewRequest(http.MethodPost, p.server.URL+adminSignupPlanPath, strings.NewReader(waitingForm.Encode())).
		WithContext(context.WithValue(ctx, privateHTTPSKey{}, true))
	waitingRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	waitingRequest.Header.Set("Origin", p.server.URL)
	waitingRequest.Header.Set("Cookie", p.nativeRequest(operator, adminSignupPlanPath).Header.Get("Cookie"))
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		p.app.routes().ServeHTTP(response, waitingRequest)
		result <- response
	}()
	until, cancel := context.WithDeadline(ctx, cutoff)
	defer cancel()
	for {
		var blocked bool
		err = db.QueryRowContext(until, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&blocked)
		if err != nil {
			t.Fatal("no admitted save was seen waiting on the real claimant lock before ticket expiry")
		}
		if blocked {
			break
		}
		select {
		case got := <-result:
			t.Fatalf("save returned %d before the PostgreSQL lock overlap", got.Code)
		case <-time.After(10 * time.Millisecond):
		}
	}
	p.app.signupPlanAdmin.mu.Lock()
	_, stillStaged := p.app.signupPlanAdmin.tickets[waitingKey]
	p.app.signupPlanAdmin.mu.Unlock()
	if stillStaged {
		t.Fatal("the tested ticket was never admitted")
	}
	time.Sleep(time.Until(cutoff) + 20*time.Millisecond)
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.Code != 404 {
			t.Fatalf("save crossed its ticket deadline, status %d", got.Code)
		}
	case <-ctx.Done():
		t.Fatal("expired save did not finish")
	}
	if snapshot() != beforeWait {
		t.Fatal("admitted then expired save wrote durable rows")
	}
	post(operator, waitingForm, 404)
	t.Log("admitted protected plan ticket expired while blocked on an actual PostgreSQL claimant lock; no writes")

	abandoned := review(operator, open(operator, expired.RequestRef, 200), 200)
	consumedKey := review(operator, open(operator, consumed.RequestRef, 200), 200)
	post(operator, save(operator, abandoned), 404) // New case invalidates prior same-session work.
	otherClient := f.client()
	if status, _, _ := f.request(otherClient, "GET", "/__signup-fixture-sign-in/mobile", "", ""); status != 204 {
		t.Fatal("synthetic consent actor failed login")
	}
	preview, err := f.application.previewNativeOwnerConsent(f.owner("mobile", "/__fixture-review"), consumed.Intent, consumed.Audience)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.application.confirmNativeOwnerConsent(f.owner("mobile", "/__fixture-confirm"),
		nativeOwnerConfirmation{Challenge: preview.Challenge, Target: preview.Target, Accept: true}); err != nil {
		t.Fatal(err)
	}
	post(operator, save(operator, consumedKey), 404)
	open(operator, consumed.RequestRef, 404)
	// The actual owner projection contains display labels, not private
	// identifiers or any recovery authority.
	status, raw, _ := f.request(ordinary, "POST", nativeSignupReviewPath, "{}", f.server.URL)
	var display nativeSignupReview
	if status != 200 || json.Unmarshal(raw, &display) != nil || display.Organization != "Marca  de prueba" ||
		!slices.Equal(display.Branches, []string{"Centro <seguro>", "Norte  dos"}) || bytes.Contains(raw, []byte("private-request")) {
		t.Fatal("native owner did not receive the exact protected authored display")
	}
	expected := before
	expected[0]++
	expected[1]++ // Only the intentional separate synthetic native consent.
	if snapshot() != expected {
		t.Fatal("wrong/expired/stale actions created data or access")
	}
	testNativeSignupOperatorChrome(t, ctx, f, p)
	if os.Getenv("GOMAR_QA_SIGNUP_OPERATOR_BROWSER") != "" {
		expected[0]++
		if snapshot() != expected {
			t.Fatal("browser authored anything other than the one display plan")
		}
	}
}

func testNativeSignupOperatorChrome(t *testing.T, ctx context.Context, owner signupTestPeer, p protectedSignupPlanTest) {
	t.Helper()
	script, python := os.Getenv("GOMAR_QA_SIGNUP_OPERATOR_BROWSER"), os.Getenv("GOMAR_QA_SIGNUP_PYTHON")
	if script == "" {
		return
	}
	if !filepath.IsAbs(script) || !filepath.IsAbs(python) {
		t.Fatal("require absolute isolated browser commands")
	}
	child := exec.CommandContext(ctx, python, "-B", script, owner.server.URL, p.server.URL)
	output, err := child.CombinedOutput()
	const pass = "PASS: protected recovery operator authored exact native owner display in Chrome desktop/mobile"
	if err != nil || !bytes.Contains(output, []byte(pass)) {
		t.Fatalf("protected Chrome proof failed: %v\n%s", err, output)
	}
	t.Log(pass)
}
