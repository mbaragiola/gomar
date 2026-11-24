package main

import (
	"encoding/hex"
	"errors"
	"slices"
	"testing"
)

func TestNativeOwnerDisplayBindingV1(t *testing.T) {
	target, ok := canonicalNativeOwnerTarget(nativeOwnerTarget{Intent: "fixture-intent", Business: "fixture-business",
		Audience: "fixture-audience", RequestRef: "fixture-request", Revision: 7, Outlets: []string{"outlet-b", "outlet-a"}})
	label := "Rincón Ñandú"
	input := []nativeInstallBranch{{"outlet-b", "Norte"}, {"outlet-a", "Centro"}}
	ordered, valid := canonicalNativeOwnerDisplay(target, label, input)
	if !ok || !valid || !slices.Equal(ordered, []nativeInstallBranch{input[1], input[0]}) || input[0].Ref != "outlet-b" {
		t.Fatal("display must use canonical branch order without mutating the source")
	}
	digest := nativeOwnerDisplayDigest(19, target, label, ordered)
	if hex.EncodeToString(digest[:]) != "5680ef201b3756d56a758cc8279543e5683e300637743c48a1fe98eb01b41fa3" {
		t.Fatal("changed the durable version-1 byte format")
	}
	plan := nativeInstallDraft{Target: target, DisplayOrganization: label, Branches: ordered}
	if !matchesNativeOwnerDisplay(digest[:], 19, plan) || matchesNativeOwnerDisplay(nil, 19, plan) ||
		matchesNativeOwnerDisplay(digest[:], 20, plan) {
		t.Fatal("unbound or wrong actor, or exact display did not match")
	}
	for _, change := range []func(*nativeInstallDraft){
		func(d *nativeInstallDraft) { d.DisplayOrganization = "Rincón Ñandú" }, // visually similar, different Unicode bytes
		func(d *nativeInstallDraft) {
			d.Branches[0].Name, d.Branches[1].Name = d.Branches[1].Name, d.Branches[0].Name
		},
		func(d *nativeInstallDraft) { d.Branches[0].Ref = "outlet-c" },
		func(d *nativeInstallDraft) { d.Target.Business += "-changed" },
		func(d *nativeInstallDraft) { d.Target.Audience += "-changed" },
		func(d *nativeInstallDraft) { d.Target.Intent += "-changed" },
		func(d *nativeInstallDraft) { d.Target.RequestRef += "-changed" },
		func(d *nativeInstallDraft) { d.Target.Revision++ },
	} {
		altered := plan
		altered.Branches = slices.Clone(plan.Branches)
		change(&altered)
		if matchesNativeOwnerDisplay(digest[:], 19, altered) {
			t.Fatal("changed actor/scope/display retained the same evidence")
		}
	}
	for _, labels := range [][]nativeInstallBranch{
		{{"outlet-b", "Centro"}, {"outlet-a", "Centro"}},
		{{"outlet-x", "Norte"}, {"outlet-a", "Centro"}},
	} {
		if _, ok := canonicalNativeOwnerDisplay(target, label, labels); ok {
			t.Fatal("duplicate or wrong display branch accepted")
		}
	}
}

func TestPostgresNativeOwnerDisplayMigration(t *testing.T) {
	ctx, db := testPostgresDB(t)
	const previous = 41
	if len(postgresMigrationNames) < previous+1 || postgresMigrationNames[previous-1] != "041_native_owner_dormant_install.sql" ||
		postgresMigrationNames[previous] != "042_native_owner_display_binding.sql" {
		t.Fatal("042 must follow 041")
	}
	if err := migratePostgresThrough(ctx, db, postgresMigrationNames[:previous]); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.users(id,username,password,active,staff,superuser)
		VALUES (901,'fixture-old-owner','!',true,false,false),(902,'fixture-old-recovery','test',true,true,true);
		INSERT INTO gomar.native_owner_intents VALUES('old-intent','old-business','old-audience');
		INSERT INTO gomar.native_owner_designations VALUES('old-intent',1,'old-request',901,902,
			ARRAY['old-branch'],NOW()-INTERVAL '1 minute',NOW()+INTERVAL '5 minutes');`)
	approvalRef, receiptRef, installRef := "approval-"+newProviderRandom(), "accept-"+newProviderRandom(), "pending-"+newProviderRandom()
	exec(`INSERT INTO gomar.native_owner_approvals VALUES('old-intent',1,$1,902,NOW()-INTERVAL '1 minute',NOW()+INTERVAL '5 minutes')`, approvalRef)
	exec(`INSERT INTO gomar.native_owner_previews VALUES(decode(repeat('5a',32),'hex'),decode(repeat('6b',32),'hex'),
		'old-intent',1,$1,NOW()-INTERVAL '1 minute',NOW()+INTERVAL '3 minutes')`, approvalRef)
	exec(`INSERT INTO gomar.native_owner_receipts VALUES($1,decode(repeat('5a',32),'hex'),'old-intent',1,$2,NOW(),NOW()+INTERVAL '3 minutes')`, receiptRef, approvalRef)
	exec(`INSERT INTO gomar.native_owner_install_drafts VALUES('old-intent',$1,902,NOW(),'Razón social anterior',
		'00000009113','old-initiator','old-claimant','[{"ref":"old-branch","name":"Local anterior"}]')`, receiptRef)
	exec(`INSERT INTO gomar.native_owner_pending_installs VALUES('old-intent',$1,$2,'00000009113',901,'administration',
		'old-issuer','old-recipient',decode(repeat('7c',32),'hex'),NOW(),NOW()+INTERVAL '2 seconds',NOW())`, receiptRef, installRef)
	exec(`INSERT INTO gomar.native_owner_pending_branches VALUES($1,'old-branch','Local anterior')`, installRef)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := verifyPostgresMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var unchanged bool
	err := db.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM gomar.native_owner_receipts WHERE receipt_ref=$1 AND approval_ref=$2 AND display_digest IS NULL)
		AND EXISTS (SELECT 1 FROM gomar.native_owner_install_drafts WHERE consent_ref=$1 AND legal_name='Razón social anterior' AND display_organization IS NULL)
		AND EXISTS (SELECT 1 FROM gomar.native_owner_pending_installs WHERE install_ref=$3 AND consent_ref=$1)
		AND EXISTS (SELECT 1 FROM gomar.native_owner_pending_branches WHERE install_ref=$3 AND branch_name='Local anterior')
		AND NOT EXISTS (SELECT 1 FROM gomar.entities)
		AND NOT EXISTS (SELECT 1 FROM gomar.outlets)
		AND NOT EXISTS (SELECT 1 FROM gomar.memberships)
		AND NOT EXISTS (SELECT 1 FROM gomar.outlet_grants)`, receiptRef, approvalRef, installRef).Scan(&unchanged)
	if err != nil || !unchanged {
		t.Fatal("042 changed evidence/history or created operational rows")
	}
	_, err = db.ExecContext(ctx, `UPDATE gomar.native_owner_receipts SET display_digest=decode(repeat('ab',32),'hex') WHERE receipt_ref=$1`, receiptRef)
	var constraint interface{ SQLState() string }
	if !errors.As(err, &constraint) || constraint.SQLState() != "23514" {
		t.Fatalf("legacy receipt stopped being immutable: %v", err)
	}
}
