package main

import (
	"database/sql"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPostgresSupplierRevisionFoundation(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-supplier-revision"
	const originalRef = "00000000000"
	const newRef = "00000000019"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(101,'supplier-fixture',$1,TRUE,TRUE,TRUE)`, encodePassword(password)); err != nil {
		t.Fatal("could not seed synthetic administrator")
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(101,'First fixture','00000000801',TRUE),(102,'Other fixture','00000000802',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(101,101,'Center',TRUE),(102,102,'Other',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(101,101,101,'purchasing',TRUE);
 INSERT INTO outlet_grants VALUES(101,101);
 INSERT INTO suppliers(id,entity_id,name,cuit,active,created_at) VALUES
 (101,101,'Original','00000000000',TRUE,'2026-11-20T12:00:00.000000Z'),
 (102,102,'Foreign','00000000000',TRUE,NULL)`); err != nil {
		t.Fatal("could not seed synthetic business records")
	}
	// 023 is not registered until the landing order is resolved. Rehearse
	// exactly its staged bytes without fabricating a production ledger entry.
	script, err := postgresMigrations.ReadFile("postgres_migrations/023_supplier_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, string(script)); err != nil {
		t.Fatal("supplier revision migration failed")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal("supplier revision migration did not commit")
	}

	a := app{db: db, host: "example.com"}
	handler := a.routes()
	cookies := postgresTestCookies(t, handler, "supplier-fixture", password)
	call := func(method, path, body, content string, want int) {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", content)
		r.Header.Set("Origin", "http://example.com")
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s returned %d, want %d", method, path, w.Code, want)
		}
	}
	staff := func(entity, id int, name, reference string, want int) {
		t.Helper()
		call("PUT", fmt.Sprintf("/api/v1/entities/%d/suppliers/%d", entity, id), fmt.Sprintf(`{"razon_social":%q,"cuit":%q}`, name, reference), "application/json", want)
	}
	admin := func(name string, active bool) {
		t.Helper()
		form := url.Values{"business_name": {name}}
		if active {
			form.Set("active", "on")
		}
		call("POST", adminSupplierPath+"101/change/", form.Encode(), "application/x-www-form-urlencoded", 302)
	}
	check := func(revision int64, name, reference string, active bool) {
		t.Helper()
		var rev int64
		var named, created string
		var enabled, rightReference bool
		err := db.QueryRowContext(ctx, `SELECT revision,name,created_at,active,cuit=$1 FROM suppliers WHERE entity_id=101 AND id=101`, reference).Scan(&rev, &named, &created, &enabled, &rightReference)
		if err != nil || rev != revision || named != name || created != "2026-11-20T12:00:00.000000Z" || enabled != active || !rightReference {
			t.Fatalf("supplier revision/state mismatch: revision=%d, expected=%d", rev, revision)
		}
	}
	check(1, "Original", originalRef, true)
	staff(101, 101, "Renamed", originalRef, 200)
	check(2, "Renamed", originalRef, true)
	staff(101, 101, "Renamed", originalRef, 200)
	check(2, "Renamed", originalRef, true)
	staff(101, 102, "Outside scope", originalRef, 404)
	admin("Renamed", false)
	check(3, "Renamed", originalRef, false)
	admin("Renamed", false)
	check(3, "Renamed", originalRef, false)
	admin("Original", true)
	check(4, "Original", originalRef, true)
	staff(101, 101, "Original", newRef, 200)
	check(5, "Original", newRef, true)
	staff(101, 101, "Original", originalRef, 200)
	check(6, "Original", originalRef, true)
	var unchanged bool
	if err := db.QueryRowContext(ctx, `SELECT revision=1 AND name='Foreign' AND created_at IS NULL FROM suppliers WHERE id=102`).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("foreign supplier changed")
	}
	var activeEvents int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM admin_supplier_history WHERE supplier_id=101 AND active_changed`).Scan(&activeEvents); err != nil || activeEvents != 2 {
		t.Fatal("administrator status history changed")
	}
	r := httptest.NewRequestWithContext(ctx, "POST", "http://example.com/", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	scope := documentReviewScope{101, 101, 101}
	prepare := func(scope documentReviewScope, in supplierNameInput) (supplierNamePreview, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return supplierNamePreview{}, err
		}
		defer tx.Rollback()
		return prepareSupplierName(r, tx, scope, in)
	}
	input := supplierNameInput{101, 6, "  Chat proposal  "}
	preview, err := prepare(scope, input)
	if err != nil || !preview.BusinessWide || preview.CurrentName != "Original" || preview.Name != "Chat proposal" {
		t.Fatal(preview, err)
	}
	if _, err := prepare(scope, supplierNameInput{102, 1, "Other rename"}); err == nil {
		t.Fatal("foreign supplier accepted")
	}
	if _, err := prepare(documentReviewScope{101, 101, 102}, input); err == nil {
		t.Fatal("ungranted outlet accepted")
	}
	apply := func(p supplierNamePreview, approved [32]byte, commit bool) (int64, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		rev, err := applySupplierName(r, tx, user{id: 101}, scope, p, approved)
		if err != nil || !commit {
			return rev, err
		}
		return rev, tx.Commit()
	}
	admin("Intervening", true)
	staff(101, 101, "Original", originalRef, 200)
	check(8, "Original", originalRef, true)
	if _, err := apply(preview, preview.digest(), true); err != supplierNameStale {
		t.Fatal("restored-name proposal was not stale", err)
	}
	input.Revision = 8
	preview, err = prepare(scope, input)
	if err != nil {
		t.Fatal(err)
	}
	digest := preview.digest()
	changed := preview
	changed.BusinessWide = false
	if _, err := apply(changed, digest, true); err != supplierNameStale {
		t.Fatal("changed scope effect accepted")
	}
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='read_only' WHERE id=101`); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(preview, digest, true); err != forbiddenRole {
		t.Fatal("role downgrade not rejected", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='purchasing' WHERE id=101`); err != nil {
		t.Fatal(err)
	}
	if rev, err := apply(preview, digest, false); err != nil || rev != 9 {
		t.Fatal(rev, err)
	}
	check(8, "Original", originalRef, true)
	if rev, err := apply(preview, digest, true); err != nil || rev != 9 {
		t.Fatal(rev, err)
	}
	if _, err := apply(preview, digest, true); err != supplierNameStale {
		t.Fatal("repeat not stale", err)
	}
	check(9, "Chat proposal", originalRef, true)

}
