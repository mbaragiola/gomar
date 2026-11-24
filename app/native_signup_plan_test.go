package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPostgresNativeSignupPlanResolver(t *testing.T) {
	ctx, db := testPostgresDB(t)
	const previous = 42
	if len(postgresMigrationNames) < previous+1 || postgresMigrationNames[previous-1] != "042_native_owner_display_binding.sql" ||
		postgresMigrationNames[previous] != "043_native_signup_plans.sql" {
		t.Fatal("043 must follow 042")
	}
	if err := migratePostgresThrough(ctx, db, postgresMigrationNames[:previous]); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.users(id,username,password,active,staff,superuser)
		VALUES (901,'fixture-old-owner','!',true,false,false),(902,'fixture-old-operator','test',true,true,true);
		INSERT INTO gomar.native_owner_intents VALUES('pre-043','pre-business','signup-fixture-hosted');
		INSERT INTO gomar.native_owner_designations VALUES('pre-043',1,'pre-request',901,902,
			ARRAY['pre-branch'],NOW()-INTERVAL '1 minute',NOW()+INTERVAL '5 minutes');`)
	prior := "approval-" + newProviderRandom()
	exec(`INSERT INTO gomar.native_owner_approvals VALUES('pre-043',1,$1,902,NOW()-INTERVAL '1 minute',NOW()+INTERVAL '5 minutes')`, prior)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := verifyPostgresMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var untouched bool
	err := db.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM gomar.native_signup_plans)
		AND EXISTS (SELECT 1 FROM gomar.native_owner_approvals WHERE intent='pre-043' AND approval_ref=$1)
		AND NOT EXISTS (SELECT 1 FROM gomar.entities)
		AND NOT EXISTS (SELECT 1 FROM gomar.memberships)
		AND NOT EXISTS (SELECT 1 FROM gomar.outlet_grants)`, prior).Scan(&untouched)
	if err != nil || !untouched {
		t.Fatal("043 reinterpreted old evidence or created access")
	}

	f := signupFixture(t, ctx, db)
	browser := f.client()
	call := func(method, path, body string, want int) []byte {
		t.Helper()
		status, raw, _ := f.request(browser, method, path, body, f.server.URL)
		if status != want {
			t.Fatalf("%s %s: %d want %d", method, path, status, want)
		}
		if status >= 400 && (strings.Contains(string(raw), "QA Organización") || strings.Contains(string(raw), "private-")) {
			t.Fatal("denial disclosed protected data")
		}
		return raw
	}
	counters := func() (n [7]int) {
		t.Helper()
		err := db.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM gomar.native_owner_previews),(SELECT count(*) FROM gomar.native_owner_receipts),
			(SELECT count(*) FROM gomar.entities),(SELECT count(*) FROM gomar.outlets),(SELECT count(*) FROM gomar.memberships),
			(SELECT count(*) FROM gomar.outlet_grants),(SELECT count(*) FROM gomar.mcp_read_tokens)`).
			Scan(&n[0], &n[1], &n[2], &n[3], &n[4], &n[5], &n[6])
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	const recordPath = "/admin/__signup-fixture-plan"
	record := func(target nativeOwnerTarget, plan nativeSignupPlan) error {
		return f.ops.recordNativeSignupPlan(f.recovery(recordPath), target, plan)
	}
	expectDeny := func(err error) {
		t.Helper()
		if !errors.Is(err, nativeOwnerConsentUnavailable) {
			t.Fatalf("expected exact scoped denial: %v", err)
		}
	}
	wantPreview := func(expected string) nativeSignupReview {
		t.Helper()
		var got nativeSignupReview
		body := call("POST", "/api/v1/alta/revisar", "{}", 200)
		if json.Unmarshal(body, &got) != nil || got.Organization != expected || !providerRandom(got.Challenge) {
			t.Fatal("wrong persisted safe display")
		}
		return got
	}
	deniedBrowser := func(challenge string) {
		t.Helper()
		before := counters()
		call("GET", "/alta", "", 404)
		call("POST", "/api/v1/alta/estado", "{}", 404)
		call("POST", "/api/v1/alta/revisar", "{}", 404)
		call("POST", "/api/v1/alta/confirmar", `{"challenge":"`+challenge+`","confirm":true}`, 404)
		if counters() != before {
			t.Fatal("denied plan created preview, receipt or access")
		}
	}
	call("GET", "/__signup-fixture-sign-in/other", "", 204)
	deniedBrowser(newProviderRandom()) // ordinary consumer exists, plan absent
	wrong := signupFixtureTarget("other-wrong")
	wrong.Audience = "another-hosted-audience"
	f.designateAndApprove("other", wrong)
	if err := record(wrong, signupFixtureDisplay("wrong-audience")); err != nil {
		t.Fatal(err)
	}
	deniedBrowser(newProviderRandom()) // a recovery-authored plan, but not this audience

	target := signupFixtureTarget("other-one")
	plan := signupFixtureDisplay("other-one")
	if err := f.ops.designateNativeOwner(f.recovery("/admin/__signup-fixture-designate"), target, f.actors["other"]); err != nil {
		t.Fatal(err)
	}
	expectDeny(record(target, plan)) // designation without recovery approval
	ref, err := f.ops.approveNativeOwnerDesignation(f.recovery("/admin/__signup-fixture-approve"), target)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.ops.recordNativeSignupPlan(f.owner("other", recordPath), target, plan); !errors.Is(err, badCSRF) {
		t.Fatalf("ordinary session obtained recovery writer: %v", err)
	}
	public := f.recovery(recordPath)
	public = public.WithContext(context.WithValue(ctx, privateHTTPSKey{}, false))
	if err = f.ops.recordNativeSignupPlan(public, target, plan); !errors.Is(err, badCSRF) {
		t.Fatalf("public ingress obtained protected writer: %v", err)
	}
	renamed := plan
	renamed.Organization += " cambiada"
	foreign := target
	foreign.Audience = wrong.Audience
	expectDeny(record(foreign, plan))
	if err = record(target, plan); err != nil {
		t.Fatal(err)
	}
	expectDeny(record(target, renamed)) // one immutable plan per revision
	if _, err = db.ExecContext(ctx, `UPDATE gomar.native_signup_plans SET organization='Not a recovery revision' WHERE intent=$1`, target.Intent); err == nil {
		t.Fatal("plan update was not rejected")
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM gomar.native_signup_plans WHERE intent=$1`, target.Intent); err == nil {
		t.Fatal("plan delete was not rejected")
	}
	first := wantPreview(plan.Organization)

	// Actual overlap: the author of a different intent cannot widen the
	// selection between the browser's shared actor lock and transaction end.
	competitor := signupFixtureTarget("other-two")
	competingRef := f.designateAndApprove("other", competitor)
	session, err := f.application.nativeOwnerBrowser(f.owner("other", "/__fixture-review"))
	if err != nil {
		t.Fatal(err)
	}
	tx, live, err := f.application.beginNativeOwnerBrowser(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exact, err := loadNativeSignupSelection(ctx, tx, live, target.Audience)
	if err != nil || exact.State != nativeSignupReviewable || exact.Plan.Organization != plan.Organization ||
		!sameNativeOwnerTarget(exact.Designation.Target, target) {
		t.Fatal("one current plan was not resolved under the browser actor lock")
	}
	var pid int
	if err = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() { second <- record(competitor, signupFixtureDisplay("other-two")) }()
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		var blocked bool
		err = db.QueryRowContext(deadline, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE a.datname=current_database()
			AND $1=ANY(pg_blocking_pids(a.pid)))`, pid).Scan(&blocked)
		if err != nil {
			t.Fatal("competing plan writer did not wait for the verified actor transaction")
		}
		if blocked {
			break
		}
		select {
		case err := <-second:
			t.Fatalf("competing plan write raced the single-plan selection: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = <-second; err != nil {
		t.Fatal(err)
	}
	deniedBrowser(first.Challenge) // both present; do not pick newest or the reviewed one
	revoke := func(scope nativeOwnerTarget, approval string) {
		t.Helper()
		if err := f.ops.revokeNativeOwnerApproval(f.recovery("/admin/__signup-fixture-revoke"), scope, approval); err != nil {
			t.Fatal(err)
		}
	}
	revoke(competitor, competingRef)
	if got := wantPreview(plan.Organization); got.Challenge == first.Challenge {
		t.Fatal("review should produce a fresh challenge")
	}
	revoke(target, ref)
	deniedBrowser(first.Challenge)

	expiring := signupFixtureTarget("other-expiring")
	if err = f.ops.designateNativeOwner(f.recovery("/admin/__signup-fixture-designate"), expiring, f.actors["other"]); err != nil {
		t.Fatal(err)
	}
	// Disposable clock-bound fixture: shorten only the synthetic approval
	// at insertion, then write through the same protected recovery method.
	var end time.Time
	err = db.QueryRowContext(ctx, `INSERT INTO gomar.native_owner_approvals
		SELECT $1,$2,$3,operator_id,NOW(),NOW()+INTERVAL '2 seconds'
		FROM gomar.native_owner_approvals WHERE approval_ref=$4 RETURNING expires_at`, expiring.Intent, expiring.Revision,
		"approval-"+newProviderRandom(), ref).Scan(&end)
	if err != nil {
		t.Fatal(err)
	}
	if err = record(expiring, signupFixtureDisplay("expiry")); err != nil {
		t.Fatal(err)
	}
	expiringPreview := wantPreview(signupFixtureDisplay("expiry").Organization)
	time.Sleep(time.Until(end) + 20*time.Millisecond)
	deniedBrowser(expiringPreview.Challenge)
	t.Log("no backfill; protected immutable author, wrong audience, actor lock overlap, ambiguity, revocation and expiration verified")
}
