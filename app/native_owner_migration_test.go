package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostgresNativeOwnerConsentMigration(t *testing.T) {
	ctx, db := testPostgresDB(t)
	const previous = 39
	if len(postgresMigrationNames) < previous+1 || postgresMigrationNames[previous-1] != "039_pos_sale_till_time.sql" ||
		postgresMigrationNames[previous] != "040_native_owner_consent.sql" {
		t.Fatal("the native receipt migration must explicitly follow 039")
	}
	if err := migratePostgresThrough(ctx, db, postgresMigrationNames[:previous]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO gomar.users(id,username,password,active,staff,superuser)
		VALUES(900,'retained-local','unchanged-test-password',true,false,false);
		INSERT INTO gomar.entities(id,name,cuit,active) VALUES(900,'Existing','00000000900',true);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES(900,900,'Existing outlet',true);
		INSERT INTO gomar.memberships(id,user_id,entity_id,role,active) VALUES(900,900,900,'accounting',true);
		INSERT INTO gomar.outlet_grants VALUES(900,900);`); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('gomar.native_owner_intents') IS NOT NULL").Scan(&present); err != nil || present {
		t.Fatalf("unexpected schema on 039: %t %v", present, err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := verifyPostgresMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var empty, existing bool
	if err := db.QueryRowContext(ctx, `SELECT
	  NOT EXISTS (SELECT 1 FROM gomar.native_owner_intents)
	   AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_designations)
	   AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_approvals)
	   AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_approval_revocations)
	   AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_previews)
	   AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_receipts),
	  EXISTS (SELECT 1 FROM gomar.users u JOIN gomar.memberships m ON m.user_id=u.id
	    JOIN gomar.entities e ON e.id=m.entity_id JOIN gomar.outlet_grants g ON g.membership_id=m.id
	    JOIN gomar.outlets o ON o.id=g.outlet_id AND o.entity_id=e.id
	    WHERE u.id=900 AND u.password='unchanged-test-password' AND u.active AND NOT u.staff AND NOT u.superuser
	    AND e.id=900 AND e.active AND o.id=900 AND o.active AND m.active AND m.role='accounting')`).Scan(&empty, &existing); err != nil || !empty || !existing {
		t.Fatalf("040 changed existing access or backfilled evidence: empty=%t preserved=%t err=%v", empty, existing, err)
	}
	var canonical, unsafe bool
	if err := db.QueryRowContext(ctx, `SELECT gomar.native_owner_outlets_are_exact(ARRAY['a','b']),
		gomar.native_owner_outlets_are_exact(ARRAY['b','a']) OR gomar.native_owner_outlets_are_exact(ARRAY['a','a'])
		OR gomar.native_owner_outlets_are_exact(ARRAY['a',NULL])`).Scan(&canonical, &unsafe); err != nil || !canonical || unsafe {
		t.Fatalf("wrong canonical outlet DB constraint: canonical=%t accepted invalid=%t err=%v", canonical, unsafe, err)
	}
	routes := (&app{db: db, host: "example.com", enableMCP: true}).routes()
	for _, path := range []string{"/api/v1/native-owner-fixture/preview", "/api/v1/native-owner-fixture/accept",
		"/api/v1/native-owner-fixture/receipt", "/api/v1/mcp/native-owner-consent"} {
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "https://example.com"+path, nil).WithContext(ctx))
		if w.Code != http.StatusNotFound {
			t.Fatalf("unregistered route %s returned %d", path, w.Code)
		}
	}
}
