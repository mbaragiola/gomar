package main

import (
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestPostgresSavedPOSTableCheck(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgresThrough(ctx, db, postgresMigrationNames[:slices.Index(postgresMigrationNames, "035_pos_table_check_retirement.sql")]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO users(id,username,password,active,staff,superuser) VALUES
 (51,'qa-cashier','not-logged-in',TRUE,FALSE,FALSE);
INSERT INTO entities(id,name,cuit,active) VALUES (51,'QA A','00000000001',TRUE),(52,'QA B','00000000002',TRUE);
INSERT INTO outlets(id,entity_id,name,active) VALUES (51,51,'QA A1',TRUE),(52,51,'QA A2',TRUE),(53,52,'QA B1',TRUE);
INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES (51,51,51,'administration',TRUE);
INSERT INTO outlet_grants VALUES (51,51);
INSERT INTO pos_products(id,outlet_id,current_revision) VALUES (51,51,1),(53,53,1);
INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES
 (51,51,1,'Pan reservado',1250,TRUE),(53,53,1,'Privado de otra entidad',900,TRUE);
WITH prior AS (
 INSERT INTO pos_table_checks(outlet_id,table_number,actor_id,request_id,request_fingerprint,total_minor)
 VALUES (51,8,51,'00112233-4455-4677-8899-aabbccdde000',decode(repeat('00',32),'hex'),1250)
 RETURNING id,outlet_id)
INSERT INTO pos_table_check_items(check_id,outlet_id,product_id,revision,quantity)
 SELECT id,outlet_id,51,1,1 FROM prior;`); err != nil {
		t.Fatal(err)
	}

	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	var backfilled int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pos_table_check_slots s JOIN pos_table_checks c
 ON c.id=s.check_id AND c.outlet_id=s.outlet_id AND c.table_number=s.table_number
 JOIN pos_table_check_items i ON i.check_id=c.id
 WHERE s.outlet_id=51 AND s.table_number=8 AND i.quantity=1 AND c.total_minor=1250`).Scan(&backfilled); err != nil || backfilled != 1 {
		t.Fatalf("upgrade lost the existing open check: count=%d err=%v", backfilled, err)
	}
	a := app{db: db, enablePOS: true, enablePOSTableChecks: true}
	const body = `{"id_solicitud":"00112233-4455-4677-8899-aabbccddeeff","mesa":7,"items":[{"producto_id":51,"revision":1,"cantidad":2}]}`
	m := entityMembership{entity: entity{ID: 51}, id: 51, userID: 51, role: "administration"}
	type answer struct {
		status int
		value  any
		err    error
	}
	call := func(outlet string, actor entityMembership, input string, read bool) answer {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return answer{err: err}
		}
		defer tx.Rollback()
		r := httptest.NewRequestWithContext(ctx, "POST", "/local-synthetic", nil)
		r.SetPathValue("outlet", outlet)
		r.SetPathValue("table", "7")
		var response answer
		if read {
			response.status, response.value, response.err = a.readPOSTableCheck(r, tx, actor, nil)
		} else {
			response.status, response.value, response.err = a.lockOutletForOperation(a.savePOSTableCheck)(r, tx, actor, []byte(input))
		}
		if response.err == nil {
			response.err = tx.Commit()
		}
		return response
	}
	expectErr := func(got answer, wanted problem) {
		t.Helper()
		if got.err != wanted {
			t.Fatalf("failure = %v want %v", got.err, wanted)
		}
	}
	reader := m
	reader.role = "read_only"
	expectErr(call("51", reader, body, false), forbiddenRole)
	expectErr(call("52", m, body, true), missingOutlet)
	expectErr(call("51", m, strings.Replace(body, `"producto_id":51`, `"producto_id":53`, 1), false), posChangedCatalog)

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if err = lockPostgresOutlet(ctx, blocker, 51); err != nil {
		t.Fatal(err)
	}
	responses := make(chan answer, 2)
	for range 2 {
		go func() { responses <- call("51", m, body, false) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().InUse < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if db.Stats().InUse < 3 {
		t.Fatal("expected two concurrent attempts behind the outlet lock")
	}
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	results := make([]answer, 0, 2)
	for range 2 {
		select {
		case answer := <-responses:
			results = append(results, answer)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].status < results[j].status })
	r0, r1 := results[0], results[1]
	if r0.err != nil || r1.err != nil || r0.status != 200 || r1.status != 201 || !reflect.DeepEqual(r0.value, r1.value) {
		t.Fatalf("not an exact idempotent replay: %+v %+v", r0, r1)
	}
	check, ok := r0.value.(posTableCheck)
	if !ok || check.Total != 2500 || len(check.Items) != 1 {
		t.Fatalf("invalid check: %+v", check)
	}
	expectErr(call("51", m, strings.Replace(body, `"cantidad":2`, `"cantidad":3`, 1), false), posConflict)
	expectErr(call("51", m, strings.Replace(body, "aabbccddeeff", "aabbccddee00", 1), false), posTableCheckOccupied)
	if _, err = db.ExecContext(ctx, `
INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES (51,51,2,'Pan nuevo',1850,TRUE);
UPDATE pos_products SET current_revision=2 WHERE id=51 AND outlet_id=51;`); err != nil {
		t.Fatal(err)
	}
	reopened := call("51", m, "", true)
	if reopened.err != nil || !reflect.DeepEqual(reopened.value, r0.value) {
		t.Fatalf("reopening changed the quote: %+v", reopened)
	}
	if prior := call("51", m, body, false); prior.err != nil || !reflect.DeepEqual(prior.value, r0.value) {
		t.Fatalf("replay repriced: %+v", prior)
	}
	retire := func(outlet string, actor entityMembership, id, reason string) answer {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return answer{err: err}
		}
		defer tx.Rollback()
		r := httptest.NewRequestWithContext(ctx, "POST", "/local-synthetic", nil)
		r.SetPathValue("outlet", outlet)
		r.SetPathValue("table", "7")
		input := fmt.Sprintf(`{"id_cuenta":%q,"motivo":%q}`, id, reason)
		status, value, err := a.lockOutletForOperation(a.retirePOSTableCheck)(r, tx, actor, []byte(input))
		if err == nil {
			err = tx.Commit()
		}
		return answer{status, value, err}
	}
	expectErr(retire("51", reader, check.ID, "cancelled"), forbiddenRole)
	expectErr(retire("52", m, check.ID, "cancelled"), missingOutlet)
	expectErr(retire("51", m, check.ID, "paid"), badInput)
	expectErr(retire("51", m, "0"+check.ID, "cancelled"), badInput)
	first := retire("51", m, check.ID, "cancelled")
	if first.err != nil || first.status != 200 || first.value.(posTableCheck).State != "retirada" {
		t.Fatalf("retirement: %+v", first)
	}
	expectErr(call("51", m, "", true), posTableCheckMissing)
	replacement := strings.Replace(strings.Replace(body, "aabbccddeeff", "aabbccddee00", 1), `"revision":1`, `"revision":2`, 1)
	next := call("51", m, replacement, false)
	if next.err != nil || next.status != 201 || next.value.(posTableCheck).ID == check.ID {
		t.Fatalf("replacement: %+v", next)
	}
	if retry := retire("51", m, check.ID, "cancelled"); retry.err != nil || !reflect.DeepEqual(first.value, retry.value) {
		t.Fatalf("retire replay incorrectly affected new check: %+v", retry)
	}
	expectErr(retire("51", m, check.ID, "saved_in_error"), posTableCheckChanged)
	if got := call("51", m, body, false); got.err != nil || !reflect.DeepEqual(first.value, got.value) {
		t.Fatalf("old save retry returned wrong check: %+v", got)
	}
	if got := call("51", m, "", true); got.err != nil || !reflect.DeepEqual(next.value, got.value) {
		t.Fatalf("replacement lost or repriced: %+v", got)
	}
	if _, err := db.ExecContext(ctx, `UPDATE pos_table_check_retirements SET reason='saved_in_error'`); err == nil {
		t.Fatal("retirement record was mutable")
	}
	for table, want := range map[string]int{"pos_table_checks": 3, "pos_table_check_items": 3, "pos_table_check_slots": 2, "pos_table_check_retirements": 1, "pos_sales": 0, "stock_movements": 0, "ap_payments": 0, "reservation_requests": 0} {
		var count int
		if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
}
