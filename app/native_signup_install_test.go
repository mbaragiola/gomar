package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func signupFixtureInstallDraft(name string, target nativeOwnerTarget, ref, cuit string) nativeInstallDraft {
	display := signupFixtureDisplay(name)
	return nativeInstallDraft{Target: target, ConsentRef: ref, DisplayOrganization: display.Organization,
		LegalName: "Razón social verificada " + name, CUIT: cuit, Initiator: "qa-init", Claimant: "qa-owner-" + name,
		Branches: display.Branches}
}

func checkSignupDisplayAfterRestart(t *testing.T, old signupTestPeer, names []string, legacy nativeOwnerReceipt) {
	t.Helper()
	// Close the pool that wrote the receipts. Use fresh app instances and
	// a fresh pool; no browser display can carry over. The HTTPS fixture
	// is reconfigured before the caller issues any subsequent requests.
	if err := old.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := old.ctx
	db, err := connectPostgres(ctx, os.Getenv("GOMAR_LOCAL_POSTGRES_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	host := strings.TrimPrefix(old.server.URL, "https://")
	a := &app{host: host, db: db, providerAuth: &hostedProviderAuth{host: host, origin: old.server.URL}}
	ops := &app{host: host, db: db, adminRecovery: true}
	const recoveryPath = "/admin/__signup-fixture-draft"
	const ownerPath = "/__signup-fixture-dormant"
	peer := nativeInstallPeer{Issuer: "billing-qa", Recipient: "gomar-qa"}
	deny := func(caseName string, err error) {
		t.Helper()
		if !errors.Is(err, nativeOwnerConsentUnavailable) {
			t.Fatalf("%s: expected scoped denial, got %v", caseName, err)
		}
	}
	counts := func(query string, into ...any) {
		t.Helper()
		if err := db.QueryRowContext(ctx, query).Scan(into...); err != nil {
			t.Fatal(err)
		}
	}
	opsCounts := func() (n [5]int) {
		counts(`SELECT (SELECT count(*) FROM gomar.entities),(SELECT count(*) FROM gomar.outlets),
			(SELECT count(*) FROM gomar.memberships),(SELECT count(*) FROM gomar.outlet_grants),
			(SELECT count(*) FROM gomar.mcp_read_tokens)`, &n[0], &n[1], &n[2], &n[3], &n[4])
		return n
	}
	before := opsCounts()
	var beforeDrafts, beforeInstalls, beforeBranches int
	counts(`SELECT (SELECT count(*) FROM gomar.native_owner_install_drafts),
		(SELECT count(*) FROM gomar.native_owner_pending_installs),(SELECT count(*) FROM gomar.native_owner_pending_branches)`,
		&beforeDrafts, &beforeInstalls, &beforeBranches)

	status, err := a.readNativeOwnerReceipt(old.owner("legacy", ownerPath), legacy.Target, legacy.Ref)
	if err != nil || !status.Current || status.Receipt.Ref != legacy.Ref {
		t.Fatal("unbound internal consent should still have its exact readable history")
	}
	oldDraft := signupFixtureInstallDraft("legacy", legacy.Target, legacy.Ref, "00000009139")
	deny("unbound receipt cannot approve a hosted draft", ops.approveNativeInstallDraft(old.recovery(recoveryPath), oldDraft))
	// Simulate an existing 041 recovery draft (for example from a previous
	// binary): not even a convincing local name can stand in for consent.
	branchJSON, _ := json.Marshal(oldDraft.Branches)
	if _, err := db.ExecContext(ctx, `INSERT INTO gomar.native_owner_install_drafts
		(intent,consent_ref,approved_by,approved_at,legal_name,cuit,initiator_ref,claimant_ref,branch_plan,display_organization)
		SELECT $1,$2,operator_id,NOW(),$4,$5,$6,$7,$8,$9 FROM gomar.native_owner_approvals WHERE approval_ref=$3`,
		legacy.Target.Intent, legacy.Ref, legacy.ApprovalRef, oldDraft.LegalName, oldDraft.CUIT,
		oldDraft.Initiator, oldDraft.Claimant, branchJSON, oldDraft.DisplayOrganization); err != nil {
		t.Fatal(err)
	}
	calls := 0
	denyVerifier := func(context.Context, nativeInstallRequest) (nativeInstallObservation, error) {
		calls++
		return nativeInstallObservation{}, errors.New("unexpected verifier call")
	}
	_, err = a.installNativeOwnerDormant(old.owner("legacy", ownerPath), legacy.Target, legacy.Ref, peer, denyVerifier)
	deny("unbound receipt and legacy draft cannot create pending install", err)

	// Only this disposable fixture can simulate an out-of-band corrupted
	// draft. No application path can update it; the trigger stays installed.
	tamperDisplay := func(intent, display string) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `ALTER TABLE gomar.native_owner_install_drafts DISABLE TRIGGER native_owner_install_drafts_immutable`); err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE gomar.native_owner_install_drafts SET display_organization=$1 WHERE intent=$2`, display, intent)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `ALTER TABLE gomar.native_owner_install_drafts ENABLE TRIGGER native_owner_install_drafts_immutable`)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	cuits := []string{"00000009113", "00000009121", "00000009806"}
	for i, name := range names {
		target := signupFixtureTarget(name)
		var consentRef, approvalRef string
		var digest []byte
		err = db.QueryRowContext(ctx, `SELECT receipt_ref,approval_ref,display_digest FROM gomar.native_owner_receipts WHERE intent=$1`,
			target.Intent).Scan(&consentRef, &approvalRef, &digest)
		if err != nil || len(digest) != 32 {
			t.Fatalf("%s: expected persisted owner display evidence", name)
		}
		draft := signupFixtureInstallDraft(name, target, consentRef, cuits[i])
		for _, change := range []func(*nativeInstallDraft){
			func(d *nativeInstallDraft) { d.DisplayOrganization = d.LegalName },
			func(d *nativeInstallDraft) { d.DisplayOrganization += " renombrada" },
			func(d *nativeInstallDraft) {
				d.DisplayOrganization = strings.ReplaceAll(d.DisplayOrganization, "  ", " ")
			},
			func(d *nativeInstallDraft) { d.Branches[1].Name = strings.ReplaceAll(d.Branches[1].Name, "  ", " ") },
			func(d *nativeInstallDraft) { d.Branches[0].Name += " renombrado" },
			func(d *nativeInstallDraft) {
				d.Branches[0].Name, d.Branches[1].Name = d.Branches[1].Name, d.Branches[0].Name
			},
		} {
			changed := draft
			changed.Branches = slices.Clone(draft.Branches)
			change(&changed)
			deny(name+": protected renamed/swapped draft", ops.approveNativeInstallDraft(old.recovery(recoveryPath), changed))
			_, err = a.installNativeOwnerDormant(old.owner(name, ownerPath), target, consentRef, peer, denyVerifier)
			deny(name+": rejected label draft never exists", err)
		}
		unauthorized := old.owner(name, recoveryPath)
		if err = ops.approveNativeInstallDraft(unauthorized, draft); err == nil {
			t.Fatal("ordinary provider session approved a recovery draft")
		}
		if err = ops.approveNativeInstallDraft(old.recovery(recoveryPath), draft); err != nil {
			t.Fatalf("%s: exact recovered draft was rejected after restart: %v", name, err)
		}
		tamperDisplay(target.Intent, draft.LegalName)
		_, err = a.installNativeOwnerDormant(old.owner(name, ownerPath), target, consentRef, peer, denyVerifier)
		deny(name+": changed persisted label denied before verifier", err)
		tamperDisplay(target.Intent, draft.DisplayOrganization)
		if calls != 0 {
			t.Fatal("denied labels/unbound receipt reached private observation")
		}
		verified := 0
		verifier := func(_ context.Context, got nativeInstallRequest) (nativeInstallObservation, error) {
			verified++
			want := nativeInstallRequest{Peer: peer, Purpose: nativeInstallPurpose, Schema: 1, Target: target,
				ConsentRef: consentRef, ApprovalRef: approvalRef, Nonce: got.Nonce, Initiator: draft.Initiator, Claimant: draft.Claimant}
			if !sameNativeInstallRequest(got, want) || !providerRandom(got.Nonce) {
				return nativeInstallObservation{}, errors.New("wrong synthetic scope")
			}
			wire, _ := json.Marshal(got)
			for _, local := range []string{draft.DisplayOrganization, draft.LegalName, draft.CUIT, "Norte", "Centro", "digest"} {
				if bytes.Contains(wire, []byte(local)) {
					t.Fatal("private display or legal evidence left native boundary")
				}
			}
			now := nativeOwnerNow()
			return nativeInstallObservation{Binding: want, CheckedAt: now, ExpiresAt: now.Add(2 * time.Second)}, nil
		}
		// Mutate only the fixture draft after the exact preflight. The final
		// native check must fail before committing, despite a positive reply.
		_, err = a.installNativeOwnerDormant(old.owner(name, ownerPath), target, consentRef, peer,
			func(ctx context.Context, got nativeInstallRequest) (nativeInstallObservation, error) {
				decision, err := verifier(ctx, got)
				tamperDisplay(target.Intent, draft.LegalName)
				return decision, err
			})
		deny(name+": changed label after verifier", err)
		var pending bool
		if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gomar.native_owner_pending_installs WHERE intent=$1)`,
			target.Intent).Scan(&pending); err != nil || pending {
			t.Fatal("mutated draft created pending rows")
		}
		tamperDisplay(target.Intent, draft.DisplayOrganization)
		record, err := a.installNativeOwnerDormant(old.owner(name, ownerPath), target, consentRef, peer, verifier)
		if err != nil || !record.Dormant || !nativeOwnerRef(record.Ref, "pending-") || verified != 2 {
			t.Fatalf("%s: exact approved display did not create one dormant plan: %v", name, err)
		}
		again, err := a.installNativeOwnerDormant(old.owner(name, ownerPath), target, consentRef, peer, denyVerifier)
		if err != nil || again != record || calls != 0 {
			t.Fatal("replay is not immutable history only")
		}
		var display, legal, legalCUIT string
		var branchPlan []byte
		err = db.QueryRowContext(ctx, `SELECT display_organization,legal_name,cuit,
			(SELECT json_agg(json_build_object('ref',branch_ref,'name',branch_name) ORDER BY branch_ref)
			 FROM gomar.native_owner_pending_branches WHERE install_ref=$2)
			FROM gomar.native_owner_install_drafts WHERE intent=$1`, target.Intent, record.Ref).Scan(&display, &legal, &legalCUIT, &branchPlan)
		var actual []nativeInstallBranch
		if err != nil || display != draft.DisplayOrganization || legal != draft.LegalName || legalCUIT != draft.CUIT ||
			json.Unmarshal(branchPlan, &actual) != nil || !slices.Equal(actual, []nativeInstallBranch{draft.Branches[1], draft.Branches[0]}) {
			t.Fatal("exact display and distinct recovery legal fields were not preserved separately")
		}
	}
	var drafts, installs, branches int
	counts(`SELECT (SELECT count(*) FROM gomar.native_owner_install_drafts),
		(SELECT count(*) FROM gomar.native_owner_pending_installs),(SELECT count(*) FROM gomar.native_owner_pending_branches)`, &drafts, &installs, &branches)
	if calls != 0 || drafts != beforeDrafts+1+len(names) ||
		installs != beforeInstalls+len(names) || branches != beforeBranches+2*len(names) || opsCounts() != before {
		t.Fatal("unexpected private, pending or operational effects")
	}
	t.Logf("persisted signup display: %d exact protected drafts and dormant plans after fixture restart; renamed/swapped/collapsed/unbound denied; no operations", len(names))
}
