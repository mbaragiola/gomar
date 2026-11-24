package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// These paths are mounted only in the synthetic provider test. They use
// the deployed schema and real Gomar auth, not a simulated acceptance store.
func checkNativeOwnerConsent(t *testing.T, a *app, ctx context.Context, db *sql.DB,
	owner, other, otherOwnerSession []*http.Cookie, rotate func() *httptest.ResponseRecorder,
) {
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
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
	designated, otherActor := actorOf(owner), actorOf(other)
	if designated != actorOf(otherOwnerSession) || designated == otherActor || hashOf(owner) == hashOf(otherOwnerSession) {
		t.Fatal("expected two authentic sessions for one provider principal, and a separate principal")
	}
	previewPath, acceptPath := "/api/v1/native-owner-fixture/preview", "/api/v1/native-owner-fixture/accept"
	fixture := http.NewServeMux()
	fixture.HandleFunc(previewPath, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
		var input struct{ Intent, Audience string }
		body, err := readJSONBody(w, r)
		if err != nil {
			return err
		}
		if err = decodeJSON(body, &input); err != nil {
			return err
		}
		result, err := a.previewNativeOwnerConsent(r, input.Intent, input.Audience)
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
		receipt, err := a.confirmNativeOwnerConsent(r, input)
		if err != nil {
			return err
		}
		jsonResponse(w, 200, receipt)
		return nil
	}))
	type receiptRead struct {
		Target nativeOwnerTarget
		Ref    string
	}
	const readPath = "/api/v1/native-owner-fixture/receipt"
	mountReceipt := func(handler *http.ServeMux, application *app) {
		handler.HandleFunc(readPath, application.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
			var input receiptRead
			body, err := readJSONBody(w, r)
			if err != nil {
				return err
			}
			if err := decodeJSON(body, &input); err != nil {
				return err
			}
			result, err := application.readNativeOwnerReceipt(r, input.Target, input.Ref)
			if err != nil {
				return err
			}
			jsonResponse(w, 200, result)
			return nil
		}))
	}
	mountReceipt(fixture, a)
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
			if c.Name == "csrftoken" || c.Name == adminRecoveryCSRF {
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
	const audience = "gomar-hosted-qa"
	ops := &app{db: db, host: a.host, adminRecovery: true}
	recoveryCookies := nativeOwnerRecoveryFixture(t, ctx, db, ops, origin)
	protected := func(r *http.Request) { *r = *r.WithContext(context.WithValue(r.Context(), privateHTTPSKey{}, true)) }
	type amendment struct {
		Target nativeOwnerTarget
		Ref    string
	}
	const designatePath, approvePath, revokePath = "/admin/native-owner-fixture/designate", "/admin/native-owner-fixture/approve", "/admin/native-owner-fixture/revoke"
	const alternateClaimantPath = "/admin/native-owner-fixture/designate-other-test-claimant"
	for path, action := range map[string]func(*http.Request, amendment) (string, error){
		designatePath: func(r *http.Request, change amendment) (string, error) {
			// Only the authenticated provider fixture map supplies a claimant.
			return "", ops.designateNativeOwner(r, change.Target, designated)
		},
		alternateClaimantPath: func(r *http.Request, change amendment) (string, error) {
			return "", ops.designateNativeOwner(r, change.Target, otherActor)
		},
		approvePath: func(r *http.Request, change amendment) (string, error) {
			return ops.approveNativeOwnerDesignation(r, change.Target)
		},
		revokePath: func(r *http.Request, change amendment) (string, error) {
			return "", ops.revokeNativeOwnerApproval(r, change.Target, change.Ref)
		},
	} {
		fixture.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			var input amendment
			body, err := readJSONBody(w, r)
			if err == nil {
				err = decodeJSON(body, &input)
			}
			if err == nil {
				var ref string
				ref, err = action(r, input)
				if err == nil {
					jsonResponse(w, 200, map[string]string{"ref": ref})
					return
				}
			}
			failure(w, err)
		})
	}
	operate := func(path string, target nativeOwnerTarget, approval string, status int) string {
		t.Helper()
		w := call(fixture, path, amendment{target, approval}, recoveryCookies, protected)
		expect(w, status)
		if status != 200 {
			return ""
		}
		var result map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result["ref"]
	}
	firstScope := nativeOwnerTarget{Intent: "signup-fixture", Business: "business-a", Audience: audience,
		RequestRef: "private-request-first", Outlets: []string{"outlet-b", "outlet-a"}, Revision: 1}
	otherScope := nativeOwnerTarget{Intent: "another-intent", Business: "business-a", Audience: audience,
		RequestRef: "private-request-other", Outlets: []string{"outlet-a", "outlet-b"}, Revision: 1}
	staleScope := nativeOwnerTarget{Intent: "stale-intent", Business: "business-a", Audience: audience,
		RequestRef: "private-request-stale", Outlets: []string{"outlet-a"}, Revision: 1}
	requestPreview := map[string]string{"intent": firstScope.Intent, "audience": audience}
	expect(call(ops.routes(), designatePath, amendment{Target: firstScope}, recoveryCookies, protected), 404)
	expect(call(fixture, designatePath, amendment{Target: firstScope}, recoveryCookies, nil), 403)
	expect(call(fixture, designatePath, amendment{Target: firstScope}, owner, protected), 403)
	operate(designatePath, firstScope, "", 200)
	operate(designatePath, firstScope, "", 404)
	expect(call(fixture, previewPath, requestPreview, owner, nil), 404) // unapproved
	wrongRecovery := firstScope
	wrongRecovery.Audience = "another-audience"
	operate(approvePath, wrongRecovery, "", 404)
	wrongRecovery = firstScope
	wrongRecovery.Outlets = []string{"outlet-a"}
	operate(approvePath, wrongRecovery, "", 404)
	approval := operate(approvePath, firstScope, "", 200)
	if !nativeOwnerRef(approval, "approval-") {
		t.Fatal("recovery approval was not recorded")
	}
	operate(approvePath, firstScope, "", 404)
	for _, target := range []nativeOwnerTarget{otherScope, staleScope} {
		operate(designatePath, target, "", 200)
		operate(approvePath, target, "", 200)
	}
	crossRequest := otherScope
	crossRequest.Intent, crossRequest.Revision = "unrelated-intent", 1
	operate(designatePath, crossRequest, "", 404) // opaque private request cannot be reused
	crossScope := firstScope
	crossScope.Audience, crossScope.RequestRef, crossScope.Revision = "unrelated-audience", "private-request-conflict", 2
	operate(designatePath, crossScope, "", 404)
	expect(call(fixture, previewPath, map[string]string{"intent": firstScope.Intent, "audience": "unrelated-audience"}, owner, nil), 404)
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
		reply := call(fixture, previewPath, map[string]string{"intent": intent, "audience": audience}, cookies, nil)
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
	// Current recovery credentials cannot grant consent as an ordinary owner.
	adminCookies := slices.Clone(recoveryCookies)
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
		func(s *nativeOwnerTarget) { s.Audience = "different-audience" },
		func(s *nativeOwnerTarget) { s.RequestRef = "unrelated-request" },
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
	stale := preview(staleScope.Intent, owner)
	staleInput := nativeOwnerConfirmation{Challenge: stale.Challenge, Target: stale.Target, Accept: true}
	var staleApproval string
	if err := db.QueryRowContext(ctx, "SELECT approval_ref FROM gomar.native_owner_approvals WHERE intent=$1", staleScope.Intent).Scan(&staleApproval); err != nil {
		t.Fatal(err)
	}
	operate(revokePath, staleScope, approval, 404)
	operate(revokePath, staleScope, staleApproval, 200)
	confirm(staleInput, owner, 404)
	expect(call(fixture, previewPath, map[string]string{"intent": staleScope.Intent, "audience": audience}, owner, nil), 404)
	operate(approvePath, staleScope, "", 404) // cannot reactivate a revoked revision
	staleScope.Revision, staleScope.RequestRef = 2, "private-request-stale-revision-two"
	operate(designatePath, staleScope, "", 200)
	latestApproval := operate(approvePath, staleScope, "", 200)
	confirm(staleInput, owner, 404)
	expiredChallenge := newProviderRandom()
	expiredHash := sha256.Sum256([]byte(expiredChallenge))
	mustExec(`INSERT INTO gomar.native_owner_previews
		(challenge_hash,session_hash,intent,revision,approval_ref,previewed_at,expires_at)
		VALUES($1,$2,$3,2,$4,NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute')`,
		expiredHash[:], session[:], staleScope.Intent, latestApproval)
	confirm(nativeOwnerConfirmation{Challenge: expiredChallenge, Target: staleScope, Accept: true}, owner, 404)
	beforeSupersession := preview(staleScope.Intent, owner)
	staleScope.Revision, staleScope.RequestRef = 3, "private-request-stale-revision-three"
	operate(designatePath, staleScope, "", 200)
	confirm(nativeOwnerConfirmation{Challenge: beforeSupersession.Challenge, Target: beforeSupersession.Target, Accept: true}, owner, 404)
	expect(call(fixture, previewPath, map[string]string{"intent": staleScope.Intent, "audience": audience}, owner, nil), 404)
	operate(approvePath, staleScope, "", 200)

	snapshot := func() (accepted, entities, outlets, memberships, grants int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.native_owner_receipts),
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
	if !slices.Equal(statuses, []int{200, 404}) {
		t.Fatalf("one consent should win: %v, %s, %s", statuses, first.Body.String(), next.Body.String())
	}
	confirm(accept, owner, 404)
	confirm(confirmations[1], owner, 404)
	winning := first
	if winning.Code != 200 {
		winning = next
	}
	decodeReceipt := func(body []byte) nativeOwnerReceipt {
		t.Helper()
		var record nativeOwnerReceipt
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &record); err != nil || !nativeOwnerRef(record.Ref, "accept-") ||
			json.Unmarshal(body, &fields) != nil || len(fields) != 5 || fields["receipt_ref"] == nil || fields["approval_ref"] == nil ||
			fields["target"] == nil || fields["accepted_at"] == nil || fields["expires_at"] == nil {
			t.Fatal("invalid or over-broad persisted receipt projection")
		}
		return record
	}
	receipt := decodeReceipt(winning.Body.Bytes())
	if receipt.ApprovalRef != approval {
		t.Fatal("consent receipt not bound to exact approval")
	}
	read := func(handler http.Handler, record nativeOwnerReceipt, jar []*http.Cookie, status int) nativeOwnerReceiptStatus {
		t.Helper()
		w := call(handler, readPath, receiptRead{record.Target, record.Ref}, jar, nil)
		expect(w, status)
		if status != 200 {
			return nativeOwnerReceiptStatus{}
		}
		var snapshot nativeOwnerReceiptStatus
		if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil ||
			snapshot.Receipt.Ref != record.Ref || !sameNativeOwnerTarget(snapshot.Receipt.Target, record.Target) ||
			!snapshot.Receipt.AcceptedAt.Equal(record.AcceptedAt) || snapshot.CheckedAt.Before(snapshot.Receipt.AcceptedAt) {
			t.Fatal("exact history was not preserved")
		}
		return snapshot
	}
	expect(call(a.routes(), readPath, receiptRead{receipt.Target, receipt.Ref}, owner, nil), 404)
	if !read(fixture, receipt, owner, 200).Current {
		t.Fatal("new exact receipt not current")
	}
	expect(call(fixture, previewPath, requestPreview, owner, nil), 404) // cannot renew a consumed intent
	if read(fixture, receipt, otherOwnerSession, 200).Current {
		t.Fatal("another same-owner session regained current consent")
	}
	read(fixture, receipt, other, 404)
	read(fixture, receipt, adminCookies, 401)
	wrong := receipt
	wrong.Target.Business = "another-business"
	read(fixture, wrong, owner, 404)
	wrong = receipt
	wrong.Target.Audience = "unrelated-audience"
	read(fixture, wrong, owner, 404)
	operate(revokePath, firstScope, approval, 200)
	if read(fixture, receipt, owner, 200).Current {
		t.Fatal("revoked approval still current")
	}
	operate(revokePath, firstScope, approval, 404)
	operate(approvePath, firstScope, "", 404)
	expect(call(fixture, previewPath, requestPreview, owner, nil), 404)
	reopened, err := connectPostgres(ctx, os.Getenv("GOMAR_LOCAL_POSTGRES_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := http.NewServeMux()
	mountReceipt(restarted, &app{db: reopened, host: a.host, providerAuth: a.providerAuth})
	if read(restarted, receipt, owner, 200).Current {
		t.Fatal("fresh app/connection revived revoked consent")
	}
	if _, err := db.ExecContext(ctx, `UPDATE gomar.native_owner_receipts SET receipt_ref=receipt_ref WHERE receipt_ref=$1`, receipt.Ref); err == nil {
		t.Fatal("durable receipt allowed mutation")
	}
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
	w := call(fixture, acceptPath, nativeOwnerConfirmation{Challenge: renewed.Challenge, Target: renewed.Target, Accept: true}, rotated, nil)
	expect(w, 200)
	secondReceipt := decodeReceipt(w.Body.Bytes())
	if !read(fixture, secondReceipt, rotated, 200).Current {
		t.Fatal("fresh explicit consent was not current")
	}
	// Even a separate otherwise well-formed receipt row for the former
	// revision must hit the intent uniqueness boundary in PostgreSQL.
	oldKey := sha256.Sum256([]byte(beforeSupersession.Challenge))
	if _, err := db.ExecContext(ctx, `INSERT INTO gomar.native_owner_receipts
		(receipt_ref,challenge_hash,intent,revision,approval_ref,accepted_at,expires_at)
		VALUES($1,$2,$3,2,$4,NOW(),NOW()+INTERVAL '1 second')`,
		"accept-"+newProviderRandom(), oldKey[:], staleScope.Intent, latestApproval); !isUnique(err) {
		t.Fatalf("expected database to reject a second receipt for another revision: %v", err)
	}
	// An accepted intent cannot be superseded to get a second receipt,
	// even after explicit revocation and even for a distinct claimant.
	afterAcceptance := staleScope
	afterAcceptance.Revision, afterAcceptance.RequestRef = 4, "private-request-after-acceptance"
	operate(designatePath, afterAcceptance, "", 404)
	operate(alternateClaimantPath, afterAcceptance, "", 404)
	afterAcceptance.Outlets = []string{"outlet-a", "outlet-b"}
	operate(designatePath, afterAcceptance, "", 404)
	operate(approvePath, afterAcceptance, "", 404)
	operate(revokePath, staleScope, secondReceipt.ApprovalRef, 200)
	operate(designatePath, afterAcceptance, "", 404)
	operate(alternateClaimantPath, afterAcceptance, "", 404)
	operate(approvePath, staleScope, "", 404)
	expect(call(fixture, previewPath, map[string]string{"intent": staleScope.Intent, "audience": audience}, rotated, nil), 404)
	confirm(nativeOwnerConfirmation{Challenge: renewed.Challenge, Target: afterAcceptance, Accept: true}, rotated, 404)
	if read(restarted, secondReceipt, rotated, 200).Current {
		t.Fatal("revoked receipt still current")
	}
	if n, e, o, m, g := snapshot(); n != 2 || e+o+m+g != 0 {
		t.Fatalf("fresh explicit consent did not record exactly one additional approval: %d %d %d %d %d", n, e, o, m, g)
	}
	consent := func(target nativeOwnerTarget) nativeOwnerReceipt {
		operate(designatePath, target, "", 200)
		operate(approvePath, target, "", 200)
		pr := preview(target.Intent, rotated)
		w := call(fixture, acceptPath, nativeOwnerConfirmation{Challenge: pr.Challenge, Target: pr.Target, Accept: true}, rotated, nil)
		expect(w, 200)
		return decodeReceipt(w.Body.Bytes())
	}
	revoke := func(r nativeOwnerReceipt) { operate(revokePath, r.Target, r.ApprovalRef, 200) }
	t.Run("dormant_install", func(t *testing.T) {
		checkNativeDormantInstall(t, ctx, db, a, ops, designated, otherActor, rotated, other, otherOwnerSession,
			recoveryCookies, call, protected, consent, revoke)
	})
	t.Run("staged_activation_proof", func(t *testing.T) {
		checkNativeStagedActivation(t, ctx, db, a, ops, designated, rotated, other, otherOwnerSession, recoveryCookies,
			call, protected, consent, revoke)
	})
}

func nativeOwnerRecoveryFixture(t *testing.T, ctx context.Context, db *sql.DB, ops *app, origin string) []*http.Cookie {
	t.Helper()
	const username, password = "same-email@example.invalid", "synthetic-recovery-password"
	if _, err := db.ExecContext(ctx, "UPDATE gomar.users SET password=$1 WHERE username=$2", encodePassword(password), username); err != nil {
		t.Fatal(err)
	}
	ctx = context.WithValue(ctx, privateHTTPSKey{}, true)
	routes := ops.routes()
	csrf := httptest.NewRecorder()
	routes.ServeHTTP(csrf, httptest.NewRequest("GET", origin+"/admin/login/", nil).WithContext(ctx))
	if csrf.Code != 200 {
		t.Fatalf("recovery sign-in form unavailable: %d", csrf.Code)
	}
	var token string
	for _, cookie := range csrf.Result().Cookies() {
		if cookie.Name == adminRecoveryCSRF {
			token = cookie.Value
		}
	}
	if token == "" {
		t.Fatal("no isolated recovery CSRF")
	}
	form := url.Values{"username": {username}, "password": {password}, "csrfmiddlewaretoken": {token}}
	req := httptest.NewRequest("POST", origin+"/admin/login/", strings.NewReader(form.Encode())).WithContext(ctx)
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range csrf.Result().Cookies() {
		req.AddCookie(cookie)
	}
	result := httptest.NewRecorder()
	routes.ServeHTTP(result, req)
	if result.Code != 302 {
		t.Fatalf("separate recovery authentication failed: %d", result.Code)
	}
	var found bool
	cookies := result.Result().Cookies()
	for _, cookie := range cookies {
		found = found || cookie.Name == adminRecoveryCookie && cookie.Secure && cookie.HttpOnly && cookie.Path == "/admin/"
		if cookie.Name == "sessionid" {
			t.Fatal("recovery minted an ordinary app session")
		}
	}
	if !found {
		t.Fatal("no protected recovery session")
	}
	return cookies
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
