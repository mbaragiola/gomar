package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// The SQL store and HTTP paths below exist only in this synthetic test. No
// production migration, public route, CLI, MCP registration, or billing
// adapter can create a designation or collect consent.
type testNativeOwnerStore struct{}

func (testNativeOwnerStore) lockNativeOwnerDesignation(ctx context.Context, tx *sql.Tx, key string) (nativeOwnerDesignation, error) {
	var d nativeOwnerDesignation
	var outlets []byte
	d.Target.Intent = key
	err := tx.QueryRowContext(ctx, `SELECT actor_id,business,outlets,revision FROM gomar.fixture_owner_designations
		WHERE intent=$1 FOR UPDATE`, key).Scan(&d.Actor, &d.Target.Business, &outlets, &d.Target.Revision)
	if err == nil {
		err = json.Unmarshal(outlets, &d.Target.Outlets)
	}
	return d, err
}

func (testNativeOwnerStore) saveNativeOwnerPreview(ctx context.Context, tx *sql.Tx, hash [32]byte, b nativeOwnerBinding) error {
	value, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gomar.fixture_owner_consent(hash,intent,binding,expires)
		VALUES($1,$2,$3,$4)`, hash[:], b.Designation.Target.Intent, value, b.Expires)
	return err
}

func (testNativeOwnerStore) lockNativeOwnerPreview(ctx context.Context, tx *sql.Tx, hash [32]byte) (nativeOwnerBinding, error) {
	var value []byte
	var b nativeOwnerBinding
	err := tx.QueryRowContext(ctx, `SELECT binding FROM gomar.fixture_owner_consent WHERE hash=$1
		AND accepted IS NULL FOR UPDATE`, hash[:]).Scan(&value)
	if err == nil {
		err = json.Unmarshal(value, &b)
	}
	return b, err
}

func (testNativeOwnerStore) consumeNativeOwnerPreview(ctx context.Context, tx *sql.Tx, hash [32]byte, now time.Time) error {
	var consumed string
	return tx.QueryRowContext(ctx, `UPDATE gomar.fixture_owner_consent AS p SET accepted=$2 WHERE p.hash=$1
		AND p.accepted IS NULL AND p.expires>$2 AND NOT EXISTS (
			SELECT 1 FROM gomar.fixture_owner_consent AS used WHERE used.intent=p.intent AND used.accepted IS NOT NULL)
		RETURNING intent`, hash[:], now).Scan(&consumed)
}

func checkNativeOwnerConsent(t *testing.T, a *app, ctx context.Context, db *sql.DB,
	owner, other, otherOwnerSession []*http.Cookie, rotate func() *httptest.ResponseRecorder,
) {
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`CREATE TABLE gomar.fixture_owner_designations (
		intent TEXT PRIMARY KEY, actor_id BIGINT NOT NULL REFERENCES users,
		business TEXT NOT NULL, outlets JSONB NOT NULL CHECK(jsonb_typeof(outlets)='array'),
		revision BIGINT NOT NULL CHECK(revision>0)
	);
	CREATE TABLE gomar.fixture_owner_consent (
		hash BYTEA PRIMARY KEY CHECK(octet_length(hash)=32),
		intent TEXT NOT NULL REFERENCES gomar.fixture_owner_designations, binding JSONB NOT NULL,
		expires TIMESTAMPTZ NOT NULL, accepted TIMESTAMPTZ CHECK(accepted<expires)
	);
	CREATE UNIQUE INDEX fixture_owner_one_acceptance ON gomar.fixture_owner_consent(intent) WHERE accepted IS NOT NULL;`)
	hashOf := func(cookies []*http.Cookie) [32]byte {
		t.Helper()
		for _, cookie := range cookies {
			if cookie.Name == "sessionid" {
				return sha256.Sum256([]byte(cookie.Value))
			}
		}
		t.Fatal("expected provider cookie")
		return [32]byte{}
	}
	actorOf := func(cookies []*http.Cookie) int64 {
		t.Helper()
		hash := hashOf(cookies)
		var id int64
		if err := db.QueryRowContext(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1", hash[:]).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	designated := actorOf(owner)
	if designated != actorOf(otherOwnerSession) || designated == actorOf(other) || hashOf(owner) == hashOf(otherOwnerSession) {
		t.Fatal("expected two authentic sessions for one provider principal, and a separate principal")
	}
	mustExec(`INSERT INTO gomar.fixture_owner_designations VALUES
		('signup-fixture',$1,'business-a','["outlet-b","outlet-a"]',1),
		('another-intent',$1,'business-a','["outlet-a","outlet-b"]',1),
		('stale-intent',$1,'business-a','["outlet-a"]',1)`, designated)

	store := testNativeOwnerStore{}
	previewPath, acceptPath := "/api/v1/native-owner-fixture/preview", "/api/v1/native-owner-fixture/accept"
	fixture := http.NewServeMux()
	fixture.HandleFunc(previewPath, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
		var input struct{ Intent string }
		body, err := readJSONBody(w, r)
		if err != nil {
			return err
		}
		if err = decodeJSON(body, &input); err != nil {
			return err
		}
		result, err := a.previewNativeOwnerConsent(r, store, input.Intent)
		if err != nil {
			return err
		}
		jsonResponse(w, 200, result)
		return nil
	}))
	fixture.HandleFunc(acceptPath, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
		var input nativeOwnerConfirmation
		body, err := readJSONBody(w, r)
		if err != nil {
			return err
		}
		if err = decodeJSON(body, &input); err != nil {
			return err
		}
		if err = a.confirmNativeOwnerConsent(r, store, input); err != nil {
			return err
		}
		w.WriteHeader(204)
		return nil
	}))
	origin := a.providerAuth.origin
	call := func(h http.Handler, path string, input any, cookies []*http.Cookie, alter func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", origin+path, bytes.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		for _, c := range cookies {
			if c.MaxAge >= 0 {
				r.AddCookie(c)
			}
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		if alter != nil {
			alter(r)
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
		if status >= 400 && strings.Contains(w.Body.String(), "business-a") {
			t.Fatal("denied request disclosed business scope")
		}
	}
	requestPreview := map[string]string{"intent": "signup-fixture"}
	expect(call(a.routes(), previewPath, requestPreview, owner, nil), 404)
	expect(call(a.routes(), acceptPath, nativeOwnerConfirmation{}, owner, nil), 404)
	expect(call(fixture, previewPath, requestPreview, other, nil), 404)
	expect(call(fixture, previewPath, requestPreview, owner, func(r *http.Request) { r.Header.Del("X-CSRFToken") }), 403)
	expect(call(fixture, previewPath, requestPreview, owner, func(r *http.Request) { r.Header.Set("Origin", "https://different.invalid") }), 403)
	expect(call(fixture, previewPath, requestPreview, owner, func(r *http.Request) { r.Header.Set("Authorization", "Bearer ignored") }), 422)
	expect(call(fixture, previewPath, requestPreview, owner, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sessionid", Value: "ambiguous"})
	}), 401)

	preview := func(intent string, cookies []*http.Cookie) nativeOwnerPreview {
		t.Helper()
		reply := call(fixture, previewPath, map[string]string{"intent": intent}, cookies, nil)
		expect(reply, 200)
		var result nativeOwnerPreview
		if err := json.Unmarshal(reply.Body.Bytes(), &result); err != nil || !providerRandom(result.Challenge) {
			t.Fatal("invalid owner preview")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(reply.Body.Bytes(), &fields); err != nil || len(fields) != 2 || fields["challenge"] == nil || fields["target"] == nil {
			t.Fatal("preview returned unexpected fields")
		}
		return result
	}
	view := preview("signup-fixture", owner)
	second := preview("signup-fixture", owner)
	if !slices.Equal(view.Target.Outlets, []string{"outlet-a", "outlet-b"}) || view.Target.Revision != 1 {
		t.Fatal("preview did not show exact designated scope")
	}
	accept := nativeOwnerConfirmation{Challenge: view.Challenge, Target: view.Target, Accept: true}
	confirm := func(input nativeOwnerConfirmation, cookies []*http.Cookie, status int) {
		t.Helper()
		expect(call(fixture, acceptPath, input, cookies, nil), status)
	}
	confirm(accept, other, 404)
	confirm(accept, otherOwnerSession, 404)
	// A real isolated recovery cookie, even accompanied by valid app CSRF,
	// can manage operations but cannot accept on the owner's behalf.
	var root user
	if err := db.QueryRowContext(ctx, "SELECT id,username,password,staff,superuser FROM users WHERE username='same-email@example.invalid'").
		Scan(&root.id, &root.name, &root.password, &root.staff, &root.superuser); err != nil {
		t.Fatal(err)
	}
	recovery := &app{db: db, host: a.host, adminRecovery: true}
	rw := httptest.NewRecorder()
	if err := recovery.startAdminSession(rw, httptest.NewRequest("POST", origin+"/admin/login/", nil).WithContext(ctx), root); err != nil {
		t.Fatal(err)
	}
	adminCookies := rw.Result().Cookies()
	for _, cookie := range owner {
		if cookie.Name == "csrftoken" {
			adminCookies = append(adminCookies, cookie)
		}
	}
	confirm(accept, adminCookies, 401)
	noConsent := accept
	noConsent.Accept = false
	confirm(noConsent, owner, 422)
	for _, alter := range []func(*nativeOwnerTarget){
		func(s *nativeOwnerTarget) { s.Intent = "another-intent" },
		func(s *nativeOwnerTarget) { s.Business = "different-business" },
		func(s *nativeOwnerTarget) { s.Revision++ },
		func(s *nativeOwnerTarget) { s.Outlets = []string{"outlet-a"} },
		func(s *nativeOwnerTarget) { s.Outlets = []string{"outlet-a", "different-outlet"} },
		func(s *nativeOwnerTarget) { s.Outlets = []string{"extra-outlet", "outlet-a", "outlet-b"} },
	} {
		wrong := accept
		alter(&wrong.Target)
		confirm(wrong, owner, 404)
	}
	duplicate := accept
	duplicate.Target.Outlets = []string{"outlet-a", "outlet-a"}
	confirm(duplicate, owner, 422)

	session := hashOf(owner)
	for _, method := range []string{"password", "employee"} {
		mustExec("UPDATE sessions SET auth_method=$1 WHERE token_hash=$2", method, session[:])
		confirm(accept, owner, 401)
	}
	mustExec("UPDATE sessions SET auth_method='provider' WHERE token_hash=$1", session[:])
	var expiry int64
	if err := db.QueryRowContext(ctx, "SELECT expires FROM sessions WHERE token_hash=$1", session[:]).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	mustExec("UPDATE sessions SET expires=1 WHERE token_hash=$1", session[:])
	confirm(accept, owner, 404)
	mustExec("UPDATE sessions SET expires=$1 WHERE token_hash=$2", expiry, session[:])
	stale := preview("stale-intent", owner)
	staleInput := nativeOwnerConfirmation{Challenge: stale.Challenge, Target: stale.Target, Accept: true}
	mustExec("UPDATE gomar.fixture_owner_designations SET revision=2 WHERE intent='stale-intent'")
	confirm(staleInput, owner, 404)
	latest := preview("stale-intent", owner)
	latestInput := nativeOwnerConfirmation{Challenge: latest.Challenge, Target: latest.Target, Accept: true}
	staleHash := sha256.Sum256([]byte(latest.Challenge))
	mustExec(`UPDATE gomar.fixture_owner_consent SET expires=NOW()-INTERVAL '1 second',
		binding=jsonb_set(binding,'{Expires}',to_jsonb(NOW()-INTERVAL '1 second'))
		WHERE hash=$1`, staleHash[:])
	confirm(latestInput, owner, 404)

	snapshot := func() (accepted, entities, outlets, memberships, grants int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.fixture_owner_consent WHERE accepted IS NOT NULL),
			(SELECT count(*) FROM entities),(SELECT count(*) FROM outlets),
			(SELECT count(*) FROM memberships),(SELECT count(*) FROM outlet_grants)`).
			Scan(&accepted, &entities, &outlets, &memberships, &grants); err != nil {
			t.Fatal(err)
		}
		return
	}
	if n, e, o, m, g := snapshot(); n+e+o+m+g != 0 {
		t.Fatalf("denied consent had side effects: %d %d %d %d %d", n, e, o, m, g)
	}
	// Both pending clicks are valid for the same session and exact intent.
	// Overlap them to prove the transaction permits one durable acceptance.
	confirmations := []nativeOwnerConfirmation{accept, {Challenge: second.Challenge, Target: second.Target, Accept: true}}
	confirmations[1].Target.Outlets = []string{"outlet-b", "outlet-a"}
	done := make(chan *httptest.ResponseRecorder, 2)
	for _, input := range confirmations {
		go func() { done <- call(fixture, acceptPath, input, owner, nil) }()
	}
	first, next := <-done, <-done
	statuses := []int{first.Code, next.Code}
	slices.Sort(statuses)
	if !slices.Equal(statuses, []int{204, 404}) {
		t.Fatalf("one consent should win: %v, %s, %s", statuses, first.Body.String(), next.Body.String())
	}
	confirm(accept, owner, 404)
	confirm(confirmations[1], owner, 404)
	fresh := preview("stale-intent", owner)
	fromOld := nativeOwnerConfirmation{Challenge: fresh.Challenge, Target: fresh.Target, Accept: true}
	// Make the real callback stop at predecessor session deletion, after
	// it locked the owner. The consent request must wait for that owner
	// without first taking a session lock that would deadlock the callback.
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var blockerPID int
	if err = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM sessions WHERE token_hash=$1 FOR SHARE`, session[:]).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	rotations, consents := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { rotations <- rotate() }()
	rotationPID := waitNativeOwnerBlocked(t, ctx, db, blockerPID, "DELETE FROM sessions WHERE token_hash")
	go func() { consents <- call(fixture, acceptPath, fromOld, owner, nil) }()
	waitNativeOwnerBlocked(t, ctx, db, rotationPID, "FROM users WHERE id=")
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	await := func(results <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case result := <-results:
			return result
		case <-ctx.Done():
			t.Fatal("rotation/consent did not settle:", ctx.Err())
			return nil
		}
	}
	result := await(rotations)
	expect(result, 303)
	expect(await(consents), 401)
	rotated := result.Result().Cookies()
	if designated != actorOf(rotated) || hashOf(rotated) == hashOf(owner) {
		t.Fatal("provider callback must rotate ordinary session")
	}
	confirm(fromOld, owner, 401)
	confirm(fromOld, rotated, 404)
	if n, e, o, m, g := snapshot(); n != 1 || e+o+m+g != 0 {
		t.Fatalf("rotation conflict changed consent or access: %d %d %d %d %d", n, e, o, m, g)
	}
	renewed := preview("stale-intent", rotated)
	confirm(nativeOwnerConfirmation{Challenge: renewed.Challenge, Target: renewed.Target, Accept: true}, rotated, 204)
	if n, e, o, m, g := snapshot(); n != 2 || e+o+m+g != 0 {
		t.Fatalf("fresh explicit consent did not record exactly one additional approval: %d %d %d %d %d", n, e, o, m, g)
	}
}

func waitNativeOwnerBlocked(t *testing.T, ctx context.Context, db *sql.DB, blocker int, queryPart string) int {
	t.Helper()
	deadline, stop := context.WithTimeout(ctx, 4*time.Second)
	defer stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var pid int
		err := db.QueryRowContext(deadline, `SELECT COALESCE(MIN(pid),0) FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid()
			AND $1=ANY(pg_blocking_pids(pid)) AND strpos(query,$2)>0`, blocker, queryPart).Scan(&pid)
		if err != nil {
			t.Fatalf("waiting for %q: %v", queryPart, err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-tick.C:
		case <-deadline.Done():
			t.Fatalf("%q did not reach its expected lock (%d)", queryPart, blocker)
		}
	}
}
