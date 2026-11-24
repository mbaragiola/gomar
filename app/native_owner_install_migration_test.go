package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostgresDormantInstallMigration(t *testing.T) {
	ctx, db := testPostgresDB(t)
	const previous = 40
	if len(postgresMigrationNames) < previous+1 || postgresMigrationNames[previous-1] != "040_native_owner_consent.sql" ||
		postgresMigrationNames[previous] != "041_native_owner_dormant_install.sql" {
		t.Fatal("dormant installation migration must follow 040")
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
		INSERT INTO gomar.native_owner_intents VALUES('preserved-intent','preserved-business','preserved-audience');
		INSERT INTO gomar.native_owner_designations VALUES('preserved-intent',1,'preserved-request',901,902,
			ARRAY['preserved-branch'],NOW()-INTERVAL '1 minute',NOW()+INTERVAL '5 minutes');`)
	approvalRef := "approval-" + newProviderRandom()
	receiptRef := "accept-" + newProviderRandom()
	exec(`INSERT INTO gomar.native_owner_approvals VALUES('preserved-intent',1,$1,902,NOW()-INTERVAL '1 minute',NOW()+INTERVAL '5 minutes')`, approvalRef)
	exec(`INSERT INTO gomar.native_owner_previews VALUES(decode(repeat('5a',32),'hex'),decode(repeat('6b',32),'hex'),
		'preserved-intent',1,$1,NOW()-INTERVAL '1 minute',NOW()+INTERVAL '3 minutes')`, approvalRef)
	exec(`INSERT INTO gomar.native_owner_receipts VALUES($1,decode(repeat('5a',32),'hex'),'preserved-intent',1,$2,NOW(),NOW()+INTERVAL '3 minutes')`, receiptRef, approvalRef)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := verifyPostgresMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var intact, empty bool
	err := db.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM gomar.native_owner_receipts WHERE receipt_ref=$1 AND approval_ref=$2 AND intent='preserved-intent' AND revision=1),
		NOT EXISTS (SELECT 1 FROM gomar.native_owner_install_drafts)
		AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_pending_installs)
		AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_pending_branches)
		AND NOT EXISTS (SELECT 1 FROM gomar.entities)
		AND NOT EXISTS (SELECT 1 FROM gomar.outlets)
		AND NOT EXISTS (SELECT 1 FROM gomar.memberships)
		AND NOT EXISTS (SELECT 1 FROM gomar.outlet_grants)`, receiptRef, approvalRef).Scan(&intact, &empty)
	if err != nil || !intact || !empty {
		t.Fatalf("041 changed historical evidence or populated active/pending data: %t %t %v", intact, empty, err)
	}
	routes := (&app{db: db, host: "example.com", enableMCP: true}).routes()
	for _, path := range []string{"/api/v1/dormant-install-fixture/record", "/api/v1/mcp/dormant-install"} {
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "https://example.com"+path, nil).WithContext(ctx))
		if w.Code != 404 {
			t.Fatalf("default route %s returned %d", path, w.Code)
		}
	}
}
