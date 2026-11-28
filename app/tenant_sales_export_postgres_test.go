package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresTenantSalesExport(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	seed := func(stmt string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture tenant','00000000001',TRUE),(2,'Other tenant','00000000002',TRUE);
	INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Export branch',TRUE),(2,1,'Ungrant branch',TRUE),(3,2,'Other branch',TRUE);
	INSERT INTO users(id,username,password,active,staff,superuser)
	VALUES(1,'fixture-accounting','!synthetic',TRUE,FALSE,FALSE),(2,'another-tenant','!synthetic',TRUE,FALSE,FALSE);
	INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES (1,1,1,'accounting',TRUE),(2,2,2,'administration',TRUE);
	INSERT INTO outlet_grants VALUES(1,1),(2,3);
	INSERT INTO pos_sales(id,request_id,outlet_id,actor_id,total_minor,recorded_at)
	VALUES (11,'11000000-0000-4000-8000-000000000011',1,1,5,'2026-11-20T02:59:59.999Z'),
	       (12,'11000000-0000-4000-8000-000000000012',1,1,12345,'2026-11-20T03:00:00Z'),
	       (13,'11000000-0000-4000-8000-000000000013',2,1,987654,'2026-11-20T03:00:00Z'),
	       (14,'11000000-0000-4000-8000-000000000014',3,2,987654,'2026-11-20T03:00:00Z'),
	       (15,'11000000-0000-4000-8000-000000000015',1,1,5,'2026-11-21T03:00:00Z');`)
	a := &app{db: db, host: "example.com"}
	router := a.routes()
	owner := nativeDocumentSession(t, a, router, 1)
	other := nativeDocumentSession(t, a, router, 2)
	request := func(cookies []*http.Cookie) *http.Request {
		r := httptest.NewRequest("POST", "http://example.com/api/v1/fixture-export", nil).WithContext(ctx)
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		return r
	}
	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, request(owner))
	if missing.Code != 404 {
		t.Fatalf("export is unexpectedly registered: %d", missing.Code)
	}
	scope := tenantSalesExportScope{entity: 1, outlet: 1, requestID: "12000000-0000-4000-8000-000000000001"}
	const day = "2026-11-20"
	actor := user{id: 1}
	check := func(got, want error) {
		t.Helper()
		if !errors.Is(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	_, err := a.prepareTenantSalesExport(request(other), user{id: 2}, scope, day, day)
	check(err, missingEntity)
	_, err = a.prepareTenantSalesExport(request(other), actor, scope, day, day)
	check(err, unauthenticated)
	otherBranch := scope
	otherBranch.outlet = 2
	_, err = a.prepareTenantSalesExport(request(owner), actor, otherBranch, day, day)
	check(err, missingOutlet)
	_, err = a.prepareTenantSalesExport(request(owner), actor, scope, "2026-10-01", day)
	check(err, badInput)

	// A failed audit insert must leave neither a receipt nor returned bytes.
	seed(`CREATE FUNCTION fixture_reject_export() RETURNS trigger LANGUAGE plpgsql AS $test$
	BEGIN RAISE EXCEPTION 'fixture interrupted'; END; $test$;
	CREATE TRIGGER fixture_reject_export AFTER INSERT ON tenant_sales_exports FOR EACH ROW EXECUTE FUNCTION fixture_reject_export();`)
	result, err := a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	if err == nil || len(result.CSV) != 0 {
		t.Fatal("failed receipt returned an artifact")
	}
	seed("DROP TRIGGER fixture_reject_export ON tenant_sales_exports")
	result, err = a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, nil)
	expected := []byte("registrado_utc,importe_centavos_ars\n2026-11-20T03:00:00Z,12345\n")
	digest := sha256.Sum256(expected)
	if !bytes.Equal(result.CSV, expected) || result.Digest != hex.EncodeToString(digest[:]) || result.Acknowledged {
		t.Fatalf("wrong artifact or premature acknowledgment: %q %+v", result.CSV, result)
	}
	seed(`INSERT INTO pos_sales(id,request_id,outlet_id,actor_id,total_minor,recorded_at)
	VALUES(16,'11000000-0000-4000-8000-000000000016',1,1,2222,'2026-11-20T04:00:00Z')`)
	replay, err := a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, nil)
	if !bytes.Equal(replay.CSV, result.CSV) || replay.Digest != result.Digest || replay.Acknowledged {
		t.Fatal("late sales changed the prepared artifact")
	}
	_, err = a.prepareTenantSalesExport(request(owner), actor, scope, day, "2026-11-21")
	check(err, tenantSalesExportConflict)
	check(a.acknowledgeTenantSalesExport(request(owner), actor, scope, strings.Repeat("0", 64)), tenantSalesExportConflict)
	check(a.acknowledgeTenantSalesExport(request(other), user{id: 2}, scope, result.Digest), missingEntity)
	otherScope := scope
	otherScope.entity, otherScope.outlet = 2, 3
	check(a.acknowledgeTenantSalesExport(request(other), user{id: 2}, otherScope, result.Digest), missingTenantSalesExport)
	seed("UPDATE memberships SET role='read_only' WHERE id=1")
	check(a.acknowledgeTenantSalesExport(request(owner), actor, scope, result.Digest), forbiddenRole)
	seed("UPDATE memberships SET role='accounting' WHERE id=1; DELETE FROM outlet_grants WHERE membership_id=1")
	_, err = a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, missingOutlet)
	seed("INSERT INTO outlet_grants VALUES(1,1)")
	check(a.acknowledgeTenantSalesExport(request(owner), actor, scope, result.Digest), nil)
	check(a.acknowledgeTenantSalesExport(request(owner), actor, scope, result.Digest), nil)
	replay, err = a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, nil)
	if !replay.Acknowledged || !bytes.Equal(replay.CSV, result.CSV) {
		t.Fatal("explicit acknowledgment did not bind to original digest")
	}
	hosted := &app{db: db, host: "example.com", hostedDiscoveryRead: &hostedDiscoveryReadFixture{}}
	_, err = hosted.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, missingOutlet)
	withoutCSRF := request(owner)
	withoutCSRF.Header.Del("X-CSRFToken")
	check(a.acknowledgeTenantSalesExport(withoutCSRF, actor, scope, result.Digest), badCSRF)
	var receipts, acknowledgments int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM tenant_sales_exports),
		(SELECT COUNT(*) FROM tenant_sales_export_acknowledgments)`).Scan(&receipts, &acknowledgments); err != nil ||
		receipts != 1 || acknowledgments != 1 {
		t.Fatalf("wrong audit counts: %d %d %v", receipts, acknowledgments, err)
	}
}
