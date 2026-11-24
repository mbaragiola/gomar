package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func checkNativeStagedActivation(t *testing.T, ctx context.Context, db *sql.DB, a, ops *app, ownerID int64,
	owner, other, sameOwnerDifferentSession, recovery []*http.Cookie,
	call func(http.Handler, string, any, []*http.Cookie, func(*http.Request)) *httptest.ResponseRecorder,
	protected func(*http.Request), consent func(nativeOwnerTarget) nativeOwnerReceipt, revoke func(nativeOwnerReceipt),
) {
	t.Helper()
	const stagePath = "/api/v1/staged-activation-fixture/record"
	const draftPath = "/admin/staged-activation-fixture/draft"
	const installPath = "/api/v1/staged-activation-fixture/pending"
	peer := nativeInstallPeer{Issuer: "install-peer-qa", Recipient: "gomar-staging-qa"}
	type installInput struct {
		Target nativeOwnerTarget
		Ref    string
	}
	mount := func(nativeVerifier nativeInstallVerifier, stageVerifier fixtureStageVerifier) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc(draftPath, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
			var draft nativeInstallDraft
			raw, err := readJSONBody(w, r)
			if err == nil {
				err = decodeJSON(raw, &draft)
			}
			if err == nil {
				err = ops.approveNativeInstallDraft(r, draft)
			}
			if err != nil {
				return err
			}
			w.WriteHeader(204)
			return nil
		}))
		mux.HandleFunc(installPath, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
			var input installInput
			raw, err := readJSONBody(w, r)
			if err == nil {
				err = decodeJSON(raw, &input)
			}
			if err != nil {
				return err
			}
			out, err := a.installNativeOwnerDormant(r, input.Target, input.Ref, peer, nativeVerifier)
			if err != nil {
				return err
			}
			jsonResponse(w, 200, out)
			return nil
		}))
		mux.HandleFunc(stagePath, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
			var input fixtureStageInput
			raw, err := readJSONBody(w, r)
			if err == nil {
				err = decodeJSON(raw, &input)
			}
			if err != nil {
				return err
			}
			out, err := fixtureStageNativeOwner(a, r, input, stageVerifier)
			if err != nil {
				return err
			}
			jsonResponse(w, 200, out)
			return nil
		}))
		return mux
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status || strings.Contains(w.Body.String(), "Privado") {
			t.Fatalf("unexpected fixture status/private output: %d want %d", w.Code, status)
		}
	}
	exec(fixtureStageDDL)
	type stagedCase struct {
		input  fixtureStageInput
		native nativeOwnerReceipt
		draft  nativeInstallDraft
	}
	prepare := func(caseName, cuit string) stagedCase {
		t.Helper()
		receipt := consent(nativeOwnerTarget{Intent: "stage-" + caseName, Business: "org-" + caseName, Audience: "gomar-hosted-qa",
			RequestRef: "private-stage-" + caseName, Outlets: []string{"outlet-a", "outlet-b"}, Revision: 1})
		draft := nativeInstallDraft{Target: receipt.Target, ConsentRef: receipt.Ref, LegalName: "Privado " + caseName, CUIT: cuit,
			Initiator: "stage-initiator", Claimant: "stage-claimant", Branches: []nativeInstallBranch{{"outlet-a", "Privado A " + caseName}, {"outlet-b", "Privado B " + caseName}}}
		nativeVerify := func(_ context.Context, input nativeInstallRequest) (nativeInstallObservation, error) {
			// This is only the separate first-install fixture, not the
			// authority for staging operational-schema rows below.
			want := nativeInstallRequest{Peer: peer, Schema: 1, Purpose: nativeInstallPurpose, Target: receipt.Target,
				Initiator: "stage-initiator", Claimant: "stage-claimant", ConsentRef: receipt.Ref, ApprovalRef: receipt.ApprovalRef, Nonce: input.Nonce}
			if !providerRandom(input.Nonce) || !sameNativeInstallRequest(input, want) {
				return nativeInstallObservation{}, errors.New("wrong install fixture")
			}
			now := nativeOwnerNow()
			return nativeInstallObservation{Binding: want, CheckedAt: now, ExpiresAt: now.Add(time.Second)}, nil
		}
		handler := mount(nativeVerify, nil)
		expect(call(handler, draftPath, draft, recovery, protected), 204)
		w := call(handler, installPath, installInput{receipt.Target, receipt.Ref}, owner, nil)
		expect(w, 200)
		var installed nativeInstallReceipt
		if json.Unmarshal(w.Body.Bytes(), &installed) != nil || !installed.Dormant || !nativeOwnerRef(installed.Ref, "pending-") {
			t.Fatal("expected actual dormant input")
		}
		return stagedCase{fixtureStageInput{receipt.Target, receipt.Ref, installed.Ref}, receipt, draft}
	}
	var authorityCalls atomic.Int32
	verifier := func(c stagedCase) fixtureStageVerifier {
		return func(_ context.Context, input fixtureStageBinding) (fixtureStageAuthority, error) {
			authorityCalls.Add(1)
			want := fixtureStageBinding{Input: c.input, Purpose: "stage-inactive-owner-qa", Approval: c.native.ApprovalRef, Nonce: input.Nonce}
			if !providerRandom(input.Nonce) || !fixtureStageSame(want, input) {
				return fixtureStageAuthority{}, errors.New("fresh fixture authority denied")
			}
			tm := nativeOwnerNow()
			return fixtureStageAuthority{Binding: want, At: tm, Before: tm.Add(2 * time.Second)}, nil
		}
	}
	counts := func() [6]int {
		t.Helper()
		var n [6]int
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.entities),(SELECT count(*) FROM gomar.outlets),
			(SELECT count(*) FROM gomar.memberships),(SELECT count(*) FROM gomar.outlet_grants),
			(SELECT count(*) FROM gomar.fixture_staged_activations),(SELECT count(*) FROM gomar.access_events)`).
			Scan(&n[0], &n[1], &n[2], &n[3], &n[4], &n[5]); err != nil {
			t.Fatal(err)
		}
		return n
	}
	mustRejectSQL := func(q string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, q, args...)
		var state interface{ SQLState() string }
		if !errors.As(err, &state) || state.SQLState() != "23514" {
			t.Fatalf("expected quarantine/immutability denial: %v", err)
		}
	}
	good := prepare("good", "00000009806")
	unchanged := counts()
	handler := mount(nil, verifier(good))
	expect(call(a.routes(), stagePath, good.input, owner, nil), 404)
	expect(call(handler, stagePath, good.input, other, nil), 404)
	expect(call(handler, stagePath, good.input, sameOwnerDifferentSession, nil), 404)
	expect(call(mount(nil, nil), stagePath, good.input, owner, nil), 404)
	for _, mutate := range []func(*fixtureStageInput){
		func(x *fixtureStageInput) { x.Target.Outlets = []string{"outlet-a"} },
		func(x *fixtureStageInput) { x.Target.Audience = "wrong-audience" },
		func(x *fixtureStageInput) { x.Target.Revision++ },
		func(x *fixtureStageInput) { x.Target.RequestRef = "wrong-request" },
		func(x *fixtureStageInput) { x.InstallRef = "pending-" + newProviderRandom() },
	} {
		input := good.input
		mutate(&input)
		expect(call(handler, stagePath, input, owner, nil), 404)
	}
	if authorityCalls.Load() != 0 {
		t.Fatal("inexact native attempts reached authority")
	}
	for _, alter := range []func(*fixtureStageAuthority){
		func(s *fixtureStageAuthority) { s.Binding.Purpose = nativeInstallPurpose },
		func(s *fixtureStageAuthority) { s.Binding.Nonce = newProviderRandom() },
		func(s *fixtureStageAuthority) { s.Before = s.At.Add(-time.Microsecond) },
		func(s *fixtureStageAuthority) { s.At = s.At.Add(-time.Minute) },
		func(s *fixtureStageAuthority) { s.Binding.Approval = "approval-" + newProviderRandom() },
	} {
		wrong := mount(nil, func(ctx context.Context, b fixtureStageBinding) (fixtureStageAuthority, error) {
			out, err := verifier(good)(ctx, b)
			alter(&out)
			return out, err
		})
		expect(call(wrong, stagePath, good.input, owner, nil), 404)
	}
	// The old operational catalogue cannot be adopted by a synthetic
	// activation, even when its CUIT appeared after dormant installation.
	collision := prepare("collision", "00000009814")
	exec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES(9814,'independent fixture',$1,true)`, collision.draft.CUIT)
	unchanged[0]++
	expect(call(mount(nil, verifier(collision)), stagePath, collision.input, owner, nil), 404)
	var oldName string
	if err := db.QueryRowContext(ctx, "SELECT name FROM gomar.entities WHERE id=9814").Scan(&oldName); err != nil || oldName != "independent fixture" {
		t.Fatal("existing tenant not preserved")
	}
	// A child added while the external observation is running must
	// invalidate the approved plan at the actual final lock.
	extra := prepare("extra", "00000009822")
	added := mount(nil, func(ctx context.Context, b fixtureStageBinding) (fixtureStageAuthority, error) {
		out, err := verifier(extra)(ctx, b)
		exec(`INSERT INTO gomar.native_owner_pending_branches VALUES($1,'outlet-z','unexpected branch')`, extra.input.InstallRef)
		return out, err
	})
	expect(call(added, stagePath, extra.input, owner, nil), 404)
	expect(call(mount(nil, verifier(extra)), stagePath, extra.input, owner, nil), 404)
	// An otherwise equal branch set with changed private labels is still
	// not the exact approved operation. Emulate corrupted stored fixture
	// data in this disposable database only, then restore the guard.
	renamed := prepare("renamed", "00000009830")
	exec("ALTER TABLE gomar.native_owner_pending_branches DISABLE TRIGGER native_owner_pending_branches_immutable")
	exec("UPDATE gomar.native_owner_pending_branches SET branch_name='unexpected name' WHERE install_ref=$1 AND branch_ref='outlet-a'", renamed.input.InstallRef)
	exec("ALTER TABLE gomar.native_owner_pending_branches ENABLE TRIGGER native_owner_pending_branches_immutable")
	expect(call(mount(nil, verifier(renamed)), stagePath, renamed.input, owner, nil), 404)

	exec(`CREATE FUNCTION gomar.fixture_stage_rollback() RETURNS trigger LANGUAGE plpgsql AS $fixture$
	  BEGIN IF NEW.name='Privado B good' THEN RAISE EXCEPTION 'synthetic second outlet failure'; END IF; RETURN NEW; END;
	  $fixture$; CREATE TRIGGER fixture_stage_rollback BEFORE INSERT ON gomar.outlets
	  FOR EACH ROW EXECUTE FUNCTION gomar.fixture_stage_rollback();`)
	expect(call(handler, stagePath, good.input, owner, nil), 500)
	if counts() != unchanged {
		t.Fatal("second branch failure left partial operational records or receipts")
	}
	exec("DROP TRIGGER fixture_stage_rollback ON gomar.outlets")

	// Both calls reach separate synthetic fresh authority after local
	// preflight, then the native transaction can commit only one mapping.
	ready, release := make(chan struct{}, 2), make(chan struct{})
	held := mount(nil, func(ctx context.Context, b fixtureStageBinding) (fixtureStageAuthority, error) {
		out, err := verifier(good)(ctx, b)
		ready <- struct{}{}
		select {
		case <-release:
			return out, err
		case <-ctx.Done():
			return fixtureStageAuthority{}, ctx.Err()
		}
	})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { responses <- call(held, stagePath, good.input, owner, nil) }()
	}
	for range 2 {
		select {
		case <-ready:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("stage transaction held locks through authority")
		}
	}
	close(release)
	decode := func(w *httptest.ResponseRecorder) fixtureStageReceipt {
		t.Helper()
		expect(w, 200)
		var out fixtureStageReceipt
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || !nativeOwnerRef(out.Ref, "stage-") || !out.Inactive ||
			out.EntityID <= 0 || out.MemberID <= 0 || out.RecordedAt.IsZero() || len(out.Branches) != 2 {
			t.Fatal("invalid inactive staged receipt")
		}
		return out
	}
	first, second := decode(<-responses), decode(<-responses)
	same := func(a, b fixtureStageReceipt) bool {
		return a.Ref == b.Ref && a.EntityID == b.EntityID && a.MemberID == b.MemberID &&
			a.RecordedAt.Equal(b.RecordedAt) && a.Inactive == b.Inactive && slices.Equal(a.Branches, b.Branches)
	}
	if !same(first, second) {
		t.Fatal("concurrent exact history restamped")
	}
	wantCounts := unchanged
	for i, amount := range []int{1, 2, 1, 2, 1, 0} {
		wantCounts[i] += amount
	}
	if counts() != wantCounts {
		t.Fatal("stage created an inexact number of rows")
	}
	var exact bool
	if err := db.QueryRowContext(ctx, `SELECT NOT e.active AND e.cuit=$3 AND e.name=$4 AND m.user_id=$5 AND m.entity_id=e.id AND m.role='administration' AND NOT m.active
		AND (SELECT count(*) FROM gomar.outlets o WHERE o.entity_id=e.id AND NOT o.active)=2
		AND (SELECT count(*) FROM gomar.outlet_grants g JOIN gomar.outlets o ON o.id=g.outlet_id WHERE g.membership_id=m.id AND o.entity_id=e.id)=2
		FROM gomar.entities e JOIN gomar.memberships m ON m.entity_id=e.id WHERE e.id=$1 AND m.id=$2`,
		first.EntityID, first.MemberID, good.draft.CUIT, good.draft.LegalName, ownerID).Scan(&exact); err != nil || !exact {
		t.Fatal("wrong staged entity or administration mapping")
	}
	for i, b := range first.Branches {
		var matches bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gomar.outlets o WHERE o.id=$1 AND o.entity_id=$2 AND o.name=$3 AND NOT o.active)`,
			b.OutletID, first.EntityID, good.draft.Branches[i].Name).Scan(&matches); err != nil || !matches || b.BranchRef != good.draft.Branches[i].Ref {
			t.Fatal("inexact staged branch mapping")
		}
	}
	mustRejectSQL("UPDATE gomar.entities SET active=true WHERE id=$1", first.EntityID)
	mustRejectSQL("UPDATE gomar.memberships SET active=true WHERE id=$1", first.MemberID)
	mustRejectSQL("INSERT INTO gomar.outlets(entity_id,name,active) VALUES($1,'extra operation',true)", first.EntityID)
	mustRejectSQL("DELETE FROM gomar.outlet_grants WHERE membership_id=$1", first.MemberID)
	mustRejectSQL("UPDATE gomar.fixture_staged_activations SET receipt_ref=receipt_ref WHERE install_ref=$1", good.input.InstallRef)

	// Use the actual ordinary recovery catalog to try to light up the
	// staged outlet, not just a SELECT over its active flag.
	form := url.Values{"name": {good.draft.Branches[0].Name}, "active": {"on"}, "_save": {"Save"}}
	for _, cookie := range recovery {
		if cookie.Name == adminRecoveryCSRF {
			form.Set("csrfmiddlewaretoken", cookie.Value)
		}
	}
	req := httptest.NewRequest("POST", a.providerAuth.origin+adminOutletPath+strconv.FormatInt(first.Branches[0].OutletID, 10)+"/change/", strings.NewReader(form.Encode())).WithContext(ctx)
	req.Header.Set("Origin", a.providerAuth.origin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range recovery {
		req.AddCookie(cookie)
	}
	protected(req)
	w := httptest.NewRecorder()
	ops.routes().ServeHTTP(w, req)
	expect(w, 500) // the ordinary authorized write reached the fixture quarantine
	var outletActive bool
	if err := db.QueryRowContext(ctx, "SELECT active FROM gomar.outlets WHERE id=$1", first.Branches[0].OutletID).Scan(&outletActive); err != nil || outletActive {
		t.Fatal("ordinary recovery action enabled a staged outlet")
	}

	// Neither an exact retry nor an ordinary API/MCP entry point opens
	// the scope when its original approval is no longer current.
	revoke(good.native)
	if history := decode(call(mount(nil, nil), stagePath, good.input, sameOwnerDifferentSession, nil)); !same(first, history) {
		t.Fatal("revocation restamped inactive history")
	}
	expect(call(handler, stagePath, good.input, other, nil), 404)
	defaults := (&app{db: db, host: a.host, providerAuth: a.providerAuth, enableMCP: true}).routes()
	get := httptest.NewRequest("GET", a.providerAuth.origin+"/api/v1/entities", nil).WithContext(ctx)
	for _, cookie := range owner {
		get.AddCookie(cookie)
	}
	list := httptest.NewRecorder()
	defaults.ServeHTTP(list, get)
	if list.Code != 200 || !strings.Contains(list.Body.String(), "[]") || strings.Contains(list.Body.String(), "Privado") {
		t.Fatal("staged actor acquired default organization access")
	}
	mcp := call(defaults, "/api/v1/auth/mcp-token", map[string]int64{"entity_id": first.EntityID, "outlet_id": first.Branches[0].OutletID}, owner, nil)
	if mcp.Code != 403 && mcp.Code != 404 {
		t.Fatalf("staged scope issued MCP access: %d", mcp.Code)
	}
	// The quarantine is not a blanket denial of ordinary unrelated rows.
	exec("UPDATE gomar.entities SET name='independent preserved' WHERE id=9814")
	if counts() != wantCounts {
		t.Fatal("legacy/replay path changed the staged counts")
	}
	t.Log("staged activation proof: one atomic inactive mapping, exact replay, fail-closed existing native/MCP entry points")
}
