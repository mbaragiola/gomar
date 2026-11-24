package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func checkNativeDormantInstall(t *testing.T, ctx context.Context, db *sql.DB, a, ops *app, ownerID, foreignID int64,
	owner, foreign, sameOwnerDifferentSession, recovery []*http.Cookie,
	call func(http.Handler, string, any, []*http.Cookie, func(*http.Request)) *httptest.ResponseRecorder,
	protected func(*http.Request), consent func(nativeOwnerTarget) nativeOwnerReceipt, revoke func(nativeOwnerReceipt),
) {
	t.Helper()
	const draftPath = "/admin/dormant-install-fixture/approve-draft"
	const installPath = "/api/v1/dormant-install-fixture/record"
	const existingCUIT, pendingCUIT, revokedCUIT, appearingCUIT = "00000009105", "00000009113", "00000009121", "00000009139"
	peer := nativeInstallPeer{"billing-qa", "gomar-qa"}
	type installInput struct {
		Target nativeOwnerTarget
		Ref    string
	}
	mount := func(expectedPeer nativeInstallPeer, verifier nativeInstallVerifier) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc(draftPath, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
			var draft nativeInstallDraft
			body, err := readJSONBody(w, r)
			if err == nil {
				err = decodeJSON(body, &draft)
			}
			if err != nil {
				return err
			}
			if err = ops.approveNativeInstallDraft(r, draft); err != nil {
				return err
			}
			w.WriteHeader(204)
			return nil
		}))
		mux.HandleFunc(installPath, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
			var input installInput
			body, err := readJSONBody(w, r)
			if err == nil {
				err = decodeJSON(body, &input)
			}
			if err != nil {
				return err
			}
			receipt, err := a.installNativeOwnerDormant(r, input.Target, input.Ref, expectedPeer, verifier)
			if err != nil {
				return err
			}
			jsonResponse(w, 200, receipt)
			return nil
		}))
		return mux
	}
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status || (status >= 400 && strings.Contains(w.Body.String(), "Privado")) {
			t.Fatalf("invalid response status or private data disclosure: got %d want %d", w.Code, status)
		}
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Foreign operational data must be neither adopted nor projected to the
	// new owner's default UI or the existing MCP credential endpoint.
	mustExec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES(910,'Otra entidad',$1,true)`, existingCUIT)
	mustExec(`INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES(910,910,'Otro local',true)`)
	mustExec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active) VALUES(910,$1,910,'administration',true)`, foreignID)
	mustExec(`INSERT INTO gomar.outlet_grants VALUES(910,910)`)

	makeDraft := func(suffix, cuit string) (nativeOwnerReceipt, nativeInstallDraft) {
		t.Helper()
		record := consent(nativeOwnerTarget{Intent: "install-" + suffix, Business: "business-" + suffix, Audience: "gomar-hosted-qa",
			RequestRef: "private-install-" + suffix, Outlets: []string{"outlet-b", "outlet-a"}, Revision: 7})
		return record, nativeInstallDraft{Target: record.Target, ConsentRef: record.Ref, LegalName: "Privado fixture",
			CUIT: cuit, Initiator: "fixture-initiator", Claimant: "fixture-claimant",
			Branches: []nativeInstallBranch{{"outlet-b", "Privado B"}, {"outlet-a", "Privado A"}}}
	}
	receipt, draft := makeDraft("one", pendingCUIT)
	input := installInput{receipt.Target, receipt.Ref}

	// The private test peer has an independent expected binding. It does
	// not turn an arbitrary caller assertion (or the minimal Billing result)
	// into authenticated proof. No network or external provider is involved.
	var calls atomic.Int32
	var paid atomic.Bool
	paid.Store(true)
	privatePeer := func(record nativeOwnerReceipt) nativeInstallVerifier {
		return func(_ context.Context, request nativeInstallRequest) (nativeInstallObservation, error) {
			calls.Add(1)
			expected := nativeInstallRequest{Peer: peer, Purpose: "first-install-observation", Schema: 1, Target: record.Target,
				Initiator: "fixture-initiator", Claimant: "fixture-claimant", ConsentRef: record.Ref,
				ApprovalRef: record.ApprovalRef, Nonce: request.Nonce}
			if !paid.Load() || !providerRandom(request.Nonce) || !sameNativeInstallRequest(request, expected) {
				return nativeInstallObservation{}, errors.New("synthetic authenticated peer refused")
			}
			now := nativeOwnerNow()
			return nativeInstallObservation{Binding: expected, CheckedAt: now, ExpiresAt: now.Add(3 * time.Second)}, nil
		}
	}
	approvedFixture := mount(peer, privatePeer(receipt))
	expect(call(a.routes(), installPath, input, owner, nil), 404)
	expect(call(ops.routes(), draftPath, draft, recovery, protected), 404)
	expect(call(approvedFixture, installPath, input, owner, nil), 404) // no local draft
	expect(call(approvedFixture, draftPath, draft, owner, protected), 403)
	expect(call(approvedFixture, draftPath, draft, recovery, nil), 403)
	conflict := draft
	conflict.CUIT = existingCUIT
	expect(call(approvedFixture, draftPath, conflict, recovery, protected), 404)
	conflict = draft
	conflict.Branches = []nativeInstallBranch{{"outlet-x", "Privado X"}, {"outlet-a", "Privado A"}}
	expect(call(approvedFixture, draftPath, conflict, recovery, protected), 422)
	expect(call(approvedFixture, draftPath, draft, recovery, protected), 204)
	expect(call(approvedFixture, draftPath, draft, recovery, protected), 404)
	expect(call(approvedFixture, installPath, input, foreign, nil), 404)
	expect(call(approvedFixture, installPath, input, sameOwnerDifferentSession, nil), 404)
	expect(call(mount(peer, nil), installPath, input, owner, nil), 404)
	for _, mutate := range []func(*installInput){
		func(v *installInput) { v.Target.Business = "foreign-business" },
		func(v *installInput) { v.Target.Audience = "foreign-audience" },
		func(v *installInput) { v.Target.Revision++ },
		func(v *installInput) { v.Target.RequestRef = "foreign-request" },
		func(v *installInput) { v.Target.Outlets = []string{"outlet-a"} },
		func(v *installInput) { v.Ref = "accept-" + newProviderRandom() },
	} {
		bad := input
		mutate(&bad)
		expect(call(approvedFixture, installPath, bad, owner, nil), 404)
	}
	if calls.Load() != 0 {
		t.Fatal("native denials reached the private observation verifier")
	}
	expect(call(mount(nativeInstallPeer{"other-issuer", "gomar-qa"}, privatePeer(receipt)), installPath, input, owner, nil), 404)
	for _, change := range []func(*nativeInstallObservation){
		func(o *nativeInstallObservation) { o.Binding.Peer.Recipient = "foreign-recipient" },
		func(o *nativeInstallObservation) { o.Binding.Purpose = "wrong-purpose" },
		func(o *nativeInstallObservation) { o.Binding.Schema++ },
		func(o *nativeInstallObservation) { o.Binding.Nonce = newProviderRandom() },
		func(o *nativeInstallObservation) { o.Binding.Claimant = "foreign-claimant" },
		func(o *nativeInstallObservation) { o.Binding.Target.Outlets = []string{"outlet-a"} },
		func(o *nativeInstallObservation) { o.Binding.Target.Revision++ },
		func(o *nativeInstallObservation) { o.Binding.ApprovalRef = "approval-" + newProviderRandom() },
		func(o *nativeInstallObservation) { o.CheckedAt = o.CheckedAt.Add(-time.Minute) },
		func(o *nativeInstallObservation) { o.ExpiresAt = o.CheckedAt.Add(-time.Second) },
		func(o *nativeInstallObservation) { o.ExpiresAt = o.CheckedAt.Add(time.Minute) },
	} {
		denied := mount(peer, func(ctx context.Context, req nativeInstallRequest) (nativeInstallObservation, error) {
			result, err := privatePeer(receipt)(ctx, req)
			change(&result)
			return result, err
		})
		expect(call(denied, installPath, input, owner, nil), 404)
	}
	counts := func(wantInstalls, wantBranches, wantEntities int) {
		t.Helper()
		var installs, branches, entities, outlets, memberships, grants, keys int
		err := db.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM gomar.native_owner_pending_installs),(SELECT count(*) FROM gomar.native_owner_pending_branches),
			(SELECT count(*) FROM gomar.entities),(SELECT count(*) FROM gomar.outlets),(SELECT count(*) FROM gomar.memberships),
			(SELECT count(*) FROM gomar.outlet_grants),(SELECT count(*) FROM gomar.mcp_read_tokens)`).
			Scan(&installs, &branches, &entities, &outlets, &memberships, &grants, &keys)
		if err != nil || installs != wantInstalls || branches != wantBranches || entities != wantEntities ||
			outlets != 1 || memberships != 1 || grants != 1 || keys != 0 {
			t.Fatalf("unexpected durable or operational effects: %d,%d,%d,%d,%d,%d,%d: %v",
				installs, branches, entities, outlets, memberships, grants, keys, err)
		}
	}
	counts(0, 0, 1)
	mustExec(`CREATE FUNCTION gomar.fixture_reject_install_branch() RETURNS trigger LANGUAGE plpgsql AS $fixture$
		BEGIN IF NEW.branch_ref='outlet-b' THEN RAISE EXCEPTION 'test rollback'; END IF; RETURN NEW; END;
		$fixture$;
		CREATE TRIGGER fixture_reject_install_branch BEFORE INSERT ON gomar.native_owner_pending_branches
		FOR EACH ROW EXECUTE FUNCTION gomar.fixture_reject_install_branch();`)
	expect(call(approvedFixture, installPath, input, owner, nil), 500)
	counts(0, 0, 1)
	mustExec("DROP TRIGGER fixture_reject_install_branch ON gomar.native_owner_pending_branches")

	// Prove the observer runs without native locks. Revocation committed
	// during the observation must be seen in the final native transaction.
	revocable, revocableDraft := makeDraft("revocable", revokedCUIT)
	expect(call(approvedFixture, draftPath, revocableDraft, recovery, protected), 204)
	revokeWhileObserving := mount(peer, func(ctx context.Context, request nativeInstallRequest) (nativeInstallObservation, error) {
		result, err := privatePeer(revocable)(ctx, request)
		revoke(revocable)
		return result, err
	})
	expect(call(revokeWhileObserving, installPath, installInput{revocable.Target, revocable.Ref}, owner, nil), 404)
	counts(0, 0, 1)

	appearing, appearingDraft := makeDraft("appearing", appearingCUIT)
	expect(call(approvedFixture, draftPath, appearingDraft, recovery, protected), 204)
	foreignWhileObserving := mount(peer, func(ctx context.Context, request nativeInstallRequest) (nativeInstallObservation, error) {
		result, err := privatePeer(appearing)(ctx, request)
		mustExec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES(911,'Alta de otra entidad',$1,true)`, appearingCUIT)
		return result, err
	})
	expect(call(foreignWhileObserving, installPath, installInput{appearing.Target, appearing.Ref}, owner, nil), 404)
	counts(0, 0, 2)

	// Force both observers to return after both native preflights. A
	// private reversal after their observations cannot activate anything.
	seen := make(chan string, 2)
	release := make(chan struct{})
	concurrent := mount(peer, func(ctx context.Context, request nativeInstallRequest) (nativeInstallObservation, error) {
		result, err := privatePeer(receipt)(ctx, request)
		seen <- request.Nonce
		<-release
		return result, err
	})
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { done <- call(concurrent, installPath, input, owner, nil) }()
	}
	wait := func() string {
		t.Helper()
		select {
		case nonce := <-seen:
			return nonce
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("native preflights did not reach independent observation")
			return ""
		}
	}
	firstNonce, secondNonce := wait(), wait()
	paid.Store(false)
	close(release)
	if firstNonce == secondNonce {
		t.Fatal("observations reused a nonce")
	}
	decode := func(w *httptest.ResponseRecorder) nativeInstallReceipt {
		t.Helper()
		expect(w, 200)
		var result nativeInstallReceipt
		var keys map[string]json.RawMessage
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || !nativeOwnerRef(result.Ref, "pending-") ||
			!result.Dormant || result.RecordedAt.IsZero() || json.Unmarshal(w.Body.Bytes(), &keys) != nil ||
			len(keys) != 3 || keys["install_ref"] == nil || keys["recorded_at"] == nil || keys["dormant"] == nil {
			t.Fatal("pending receipt was not bounded or was not dormant")
		}
		return result
	}
	first, second := decode(<-done), decode(<-done)
	if first != second {
		t.Fatal("an exact concurrent attempt restamped dormant history")
	}
	counts(1, 2, 2)
	var plannedOwner int64
	var role, legalName, cuit string
	var branchesJSON []byte
	if err := db.QueryRowContext(ctx, `SELECT p.planned_owner_id,p.planned_role,d.legal_name,p.cuit,
		(SELECT json_agg(branch_ref ORDER BY branch_ref) FROM gomar.native_owner_pending_branches b WHERE b.install_ref=p.install_ref)
		FROM gomar.native_owner_pending_installs p JOIN gomar.native_owner_install_drafts d ON d.intent=p.intent
		WHERE p.install_ref=$1`, first.Ref).Scan(&plannedOwner, &role, &legalName, &cuit, &branchesJSON); err != nil {
		t.Fatal(err)
	}
	var exactBranches []string
	if json.Unmarshal(branchesJSON, &exactBranches) != nil || !slices.Equal(exactBranches, receipt.Target.Outlets) ||
		plannedOwner != ownerID || role != "administration" || legalName != draft.LegalName || cuit != pendingCUIT {
		t.Fatal("pending mapping did not preserve the intended owner and exact outlets")
	}
	revoke(receipt)
	if history := decode(call(mount(peer, nil), installPath, input, sameOwnerDifferentSession, nil)); history != first {
		t.Fatal("exact history changed after local and synthetic private reversal")
	}
	expect(call(approvedFixture, installPath, input, foreign, nil), 404)
	expect(call(mount(nativeInstallPeer{"other-issuer", peer.Recipient}, nil), installPath, input, owner, nil), 404)
	defaults := (&app{db: db, host: a.host, providerAuth: a.providerAuth, enableMCP: true}).routes()
	get := httptest.NewRequest("GET", a.providerAuth.origin+"/api/v1/entities", nil).WithContext(ctx)
	for _, cookie := range owner {
		get.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	defaults.ServeHTTP(w, get)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[]") || strings.Contains(w.Body.String(), "Privado") {
		t.Fatal("a dormant record became an operational entity")
	}
	wantToken := call(defaults, "/api/v1/auth/mcp-token", map[string]int{"entity_id": 910, "outlet_id": 910}, owner, nil)
	if wantToken.Code != 403 && wantToken.Code != 404 {
		t.Fatalf("scope unexpectedly admitted MCP: %d", wantToken.Code)
	}
	counts(1, 2, 2)
}
