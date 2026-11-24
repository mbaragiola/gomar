package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestPostgresManualTender(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO users(id,username,password,active,staff,superuser) VALUES
 (85,'synthetic-clerk','unused',TRUE,FALSE,FALSE),(86,'synthetic-peer','unused',TRUE,FALSE,FALSE),(87,'synthetic-reader','unused',TRUE,FALSE,FALSE);
INSERT INTO entities(id,name,cuit,active) VALUES (85,'QA tender','00000000001',TRUE),(86,'Other tenant','00000000002',TRUE);
INSERT INTO outlets(id,entity_id,name,active) VALUES (85,85,'QA one',TRUE),(86,85,'Private branch',TRUE),(87,86,'Private tenant',TRUE);
INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES
 (85,85,85,'administration',TRUE),(86,86,85,'purchasing',TRUE),(87,87,85,'read_only',TRUE);
INSERT INTO outlet_grants VALUES (85,85),(86,85),(87,85);
INSERT INTO pos_products(id,outlet_id,current_revision) VALUES (81,85,2);
INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES
 (81,85,1,'Original price',1250,TRUE),(81,85,2,'New price',1400,TRUE);
INSERT INTO stock_outlets VALUES (85,TRUE);
INSERT INTO stock_products(outlet_id,product_id,enabled,minimum) VALUES (85,81,TRUE,0);
WITH checks AS (
 INSERT INTO pos_table_checks(outlet_id,table_number,actor_id,request_id,request_fingerprint,total_minor)
 SELECT x.outlet,x.table_no,85,'00112233-4455-4677-8899-aabbccdde00' || x.table_no::text,decode(repeat('00',32),'hex'),1250
 FROM (VALUES (85,1),(85,2),(85,3),(85,4),(85,5),(86,1),(87,1)) x(outlet,table_no)
 RETURNING id,outlet_id,table_number)
INSERT INTO pos_table_check_slots(check_id,outlet_id,table_number) SELECT id,outlet_id,table_number FROM checks;
INSERT INTO pos_table_check_items(check_id,outlet_id,product_id,revision,quantity)
 SELECT check_id,outlet_id,81,1,1 FROM pos_table_check_slots WHERE outlet_id=85 AND table_number=5;`); err != nil {
		t.Fatal(err)
	}
	findCheck := func(table int) string {
		var id string
		if err := db.QueryRowContext(ctx, "SELECT check_id::text FROM pos_table_check_slots WHERE outlet_id=85 AND table_number=$1", table).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	id1, id2, id3, id4 := findCheck(1), findCheck(2), findCheck(3), findCheck(4)
	m := entityMembership{entity: entity{ID: 85}, id: 85, userID: 85, role: "administration"}
	peer := entityMembership{entity: entity{ID: 85}, id: 86, userID: 86, role: "purchasing"}
	reader := entityMembership{entity: entity{ID: 85}, id: 87, userID: 87, role: "read_only"}
	a := app{db: db, enablePOS: true, enablePOSTableChecks: true, enablePOSManualTender: true, enablePOSTableFinalization: true, enableStock: true}
	off := app{db: db, host: "example.com", enablePOS: true, enablePOSTableChecks: true}
	for _, method := range []string{"GET", "POST"} {
		res := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, method, "http://example.com/api/v1/entities/85/outlets/85/pos/table-checks/1/tender", nil)
		off.routes().ServeHTTP(res, req)
		if res.Code != 404 {
			t.Fatalf("disabled tender route answered %d to %s", res.Code, method)
		}
	}
	type response struct {
		status int
		body   any
		err    error
	}
	call := func(which string, actor entityMembership, outlet int, table int, body string) response {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return response{err: err}
		}
		defer tx.Rollback()
		r := httptest.NewRequestWithContext(ctx, "POST", "/fixture", nil)
		r.SetPathValue("outlet", fmt.Sprint(outlet))
		r.SetPathValue("table", fmt.Sprint(table))
		var handler scopedHandler
		switch which {
		case "read":
			handler = a.readPOSTender
		case "write":
			handler = a.lockPOSTenderForWrite(a.writePOSTender)
		case "finalize":
			handler = a.lockPOSTenderForWrite(a.finalizePOSTable)
		case "legacy":
			handler = lockPOSWriteScope("read_business_data", true, func(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
				return postPOSSale(r, tx, m, raw, false, time.Now)
			})
		case "retire":
			off := app{db: db, enablePOS: true, enablePOSTableChecks: true}
			handler = lockPOSWriteScope("read_business_data", true, off.retirePOSTableCheck)
		}
		code, data, err := handler(r, tx, actor, []byte(body))
		if err == nil {
			err = tx.Commit()
		}
		return response{code, data, err}
	}
	fail := func(got response, want problem) {
		t.Helper()
		if got.err != want || got.body != nil {
			t.Fatalf("response = %+v, want %v and no body", got, want)
		}
	}
	record := func(check string, suffix int, amount int, method string) string {
		return fmt.Sprintf(`{"id_cuenta":%q,"id_solicitud":"00000000-0000-4000-8000-%012x","tipo":"record","importe_centavos":%d,"medio":%q}`,
			check, suffix, amount, method)
	}
	void := func(check string, suffix int, target string, reason string) string {
		return fmt.Sprintf(`{"id_cuenta":%q,"id_solicitud":"00000000-0000-4000-8000-%012x","tipo":"void","registro_original":%q,"motivo":%q}`,
			check, suffix, target, reason)
	}
	first := record(id1, 1, 500, "efectivo")
	fail(call("write", reader, 85, 1, first), forbiddenRole)
	fail(call("read", m, 86, 1, ""), missingOutlet)
	fail(call("write", m, 87, 1, first), missingOutlet)
	fail(call("write", m, 85, 1, record(id1, 1, 500, "qr")), badInput)
	fail(call("write", m, 85, 1, first[:len(first)-1]+`,"actor_id":85}`), badInput)
	fail(call("write", m, 85, 1, first[:len(first)-1]+`,"medio":"debito"}`), badInput)
	one := call("write", m, 85, 1, first)
	if one.err != nil || one.status != 201 {
		t.Fatalf("first tender = %+v", one)
	}
	again := call("write", m, 85, 1, first)
	if again.status != 200 || !reflect.DeepEqual(one.body, again.body) {
		t.Fatalf("not exact replay: %+v", again)
	}
	fail(call("write", peer, 85, 1, first), posTableTenderConflict)
	fail(call("write", m, 85, 2, record(id2, 1, 500, "efectivo")), posTableTenderConflict)
	fail(call("write", m, 85, 1, record(id1, 2, 751, "debito")), posTableTenderOverpay)
	get := call("read", m, 85, 1, "")
	view, ok := get.body.(posTenderView)
	if !ok || view.Recorded != 500 || view.Outstanding != 750 || len(view.Events) != 1 {
		t.Fatalf("bad status: %+v", get)
	}
	correction := void(id1, 2, view.Events[0].ID, "importe_incorrecto")
	fail(call("write", m, 85, 2, void(id2, 2, view.Events[0].ID, "importe_incorrecto")), posTableTenderConflict)
	fixed := call("write", peer, 85, 1, correction)
	if fixed.status != 201 || fixed.err != nil {
		t.Fatalf("correction: %+v", fixed)
	}
	if fixReplay := call("write", peer, 85, 1, correction); fixReplay.status != 200 || !reflect.DeepEqual(fixed.body, fixReplay.body) {
		t.Fatalf("correction replay: %+v", fixReplay)
	}
	if original := call("write", m, 85, 1, first); original.status != 200 || !reflect.DeepEqual(original.body, one.body) {
		t.Fatalf("original replay: %+v", original)
	}
	fail(call("write", peer, 85, 1, void(id1, 3, view.Events[0].ID, "entrada_incorrecta")), posTableTenderConflict)
	after := call("read", m, 85, 1, "").body.(posTenderView)
	if after.Recorded != 0 || after.Outstanding != 1250 || len(after.Events) != 2 {
		t.Fatalf("correction changed wrong balance: %+v", after)
	}
	fail(call("retire", m, 85, 1, fmt.Sprintf(`{"id_cuenta":%q,"motivo":"cancelled"}`, id1)), posTableTenderBlocksRetirement)
	if got := call("retire", m, 85, 2, fmt.Sprintf(`{"id_cuenta":%q,"motivo":"cancelled"}`, id2)); got.err != nil || got.status != 200 {
		t.Fatalf("untouched branch check should retire: %+v", got)
	}
	fail(call("write", m, 85, 2, record(id2, 4, 10, "debito")), posTableCheckMissing)

	concurrent := make(chan response, 2)
	for i := range 2 {
		go func() { concurrent <- call("write", m, 85, 3, record(id3, 20+i, 900, "efectivo")) }()
	}
	a1, a2 := <-concurrent, <-concurrent
	if a2.status == 201 {
		a1, a2 = a2, a1
	}
	if a1.status != 201 || a1.err != nil || a2.err != posTableTenderOverpay {
		t.Fatalf("concurrent overpay was not guarded: %+v %+v", a1, a2)
	}
	go func() { concurrent <- call("write", m, 85, 4, record(id4, 24, 100, "efectivo")) }()
	go func() {
		concurrent <- call("retire", m, 85, 4, fmt.Sprintf(`{"id_cuenta":%q,"motivo":"cancelled"}`, id4))
	}()
	a1, a2 = <-concurrent, <-concurrent
	if a1.err != nil {
		a1, a2 = a2, a1
	}
	if a1.err != nil || !(a1.status == 201 && a2.err == posTableTenderBlocksRetirement || a1.status == 200 && a2.err == posTableCheckMissing) {
		t.Fatalf("race lost history or slot: %+v %+v", a1, a2)
	}
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if err := lockPostgresEntity(ctx, blocker, 85); err != nil {
		t.Fatal(err)
	}
	go func() { concurrent <- call("write", peer, 85, 1, record(id1, 30, 100, "efectivo")) }()
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().InUse < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if db.Stats().InUse < 2 {
		t.Fatal("expected queued clerk behind entity lock")
	}
	if _, err := blocker.ExecContext(ctx, "UPDATE memberships SET role='read_only' WHERE id=86"); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	fail(<-concurrent, forbiddenRole)

	if _, err := db.ExecContext(ctx, "UPDATE pos_table_tender_events SET method='debito' WHERE check_id=$1 AND kind='record'", id1); err == nil {
		t.Fatal("tender history unexpectedly writable")
	}
	var sales, movements int
	if err := db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM pos_sales), (SELECT count(*) FROM stock_movements)").Scan(&sales, &movements); err != nil || sales != 0 || movements != 0 {
		t.Fatalf("tender created sales or stock: %d %d %v", sales, movements, err)
	}
	b, err := json.Marshal(after)
	if err != nil || len(b) == 0 {
		t.Fatalf("tender response JSON: %s %v", b, err)
	}

	reqID := func(suffix int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", suffix) }
	finalBody := func(check, rev string, suffix int) string {
		return fmt.Sprintf(`{"id_cuenta":%q,"revision_pagos":%q,"id_solicitud":%q}`, check, rev, reqID(suffix))
	}
	legacy := func(suffix int, method string) string {
		return fmt.Sprintf(`{"id_solicitud":%q,"actor_id":85,"items":[{"producto_id":81,"revision":1,"cantidad":1}]%s}`, reqID(suffix), method)
	}
	id5 := findCheck(5)
	fail(call("finalize", m, 85, 5, finalBody(id5, "1", 40)), posTableNotCovered)
	if got := call("write", m, 85, 5, record(id5, 41, 500, "efectivo")); got.err != nil {
		t.Fatal(got.err)
	}
	if got := call("write", m, 85, 5, record(id5, 42, 750, "debito")); got.err != nil {
		t.Fatal(got.err)
	}
	history := func() []posTenderEvent { return call("read", m, 85, 5, "").body.(posTenderView).Events }
	h := history()
	old := h[len(h)-1].ID
	if got := call("write", m, 85, 5, void(id5, 43, h[0].ID, "medio_incorrecto")); got.err != nil {
		t.Fatal(got.err)
	}
	if got := call("write", m, 85, 5, record(id5, 44, 500, "transferencia")); got.err != nil {
		t.Fatal(got.err)
	}
	h = history()
	rev := h[len(h)-1].ID
	fail(call("finalize", m, 85, 5, finalBody(id5, old, 46)), posTableTenderConflict)
	body := finalBody(id5, rev, 46)
	fail(call("finalize", reader, 85, 5, body), forbiddenRole)
	fail(call("finalize", m, 86, 5, body), missingOutlet)
	fail(call("finalize", m, 87, 5, body), missingOutlet)
	employee := m
	employee.employee = true
	fail(call("finalize", employee, 85, 5, body), forbiddenRole)
	fail(call("finalize", m, 85, 5, finalBody(id5, rev, 44)), posTableTenderConflict)

	// Fault after inserting the sale and finalization rows: no partial stock,
	// finalized sale, or release may survive a rollback.
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION qa_reject_stock() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture refuses stock'; END$$;
 CREATE TRIGGER qa_reject_stock BEFORE INSERT ON stock_movements FOR EACH ROW EXECUTE FUNCTION qa_reject_stock();`); err != nil {
		t.Fatal(err)
	}
	if failed := call("finalize", m, 85, 5, body); failed.err == nil {
		t.Fatal("stock fixture did not interrupt finalization")
	}
	var counts string
	count := func() {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM pos_sales)||'|'||
 (SELECT count(*) FROM pos_table_finalizations)||'|'||(SELECT count(*) FROM stock_movements)||'|'||
 (SELECT count(*) FROM pos_table_check_slots WHERE outlet_id=85 AND table_number=5)`).Scan(&counts); err != nil {
			t.Fatal(err)
		}
	}
	count()
	if counts != "0|0|0|1" {
		t.Fatalf("failed finalization mutated state: %s", counts)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER qa_reject_stock ON stock_movements; DROP FUNCTION qa_reject_stock();`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		go func() { concurrent <- call("finalize", m, 85, 5, body) }()
	}
	x, y := <-concurrent, <-concurrent
	if y.status == 201 {
		x, y = y, x
	}
	if x.status != 201 || y.status != 200 || x.err != nil || y.err != nil || !reflect.DeepEqual(x.body, y.body) {
		t.Fatalf("two same-key confirms: %+v %+v", x, y)
	}
	count()
	if counts != "1|1|1|0" {
		t.Fatalf("duplicate finalization effects: %s", counts)
	}
	fail(call("finalize", m, 85, 5, finalBody(id5, rev, 47)), posTableCheckMissing)
	fail(call("finalize", peer, 85, 5, body), forbiddenRole)
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='purchasing' WHERE id=86"); err != nil {
		t.Fatal(err)
	}
	fail(call("finalize", peer, 85, 5, body), posConflict)
	fail(call("legacy", m, 85, 5, legacy(48, `,"metodo_pago":"mixto"`)), badInput)
	fail(call("legacy", m, 85, 5, legacy(46, "")), posConflict)
	var method, breakdown string
	if err := db.QueryRowContext(ctx, `SELECT s.payment_method,string_agg(e.method||':'||e.amount_minor,',' ORDER BY e.id)
 FROM pos_table_finalizations f JOIN pos_sales s ON s.id=f.sale_id
 JOIN pos_table_finalization_legs l ON l.check_id=f.check_id JOIN pos_table_tender_events e ON e.id=l.tender_id
 GROUP BY s.payment_method`).Scan(&method, &breakdown); err != nil || method != "mixto" || breakdown != "debito:750,transferencia:500" {
		t.Fatalf("wrong split: %s %s %v", method, breakdown, err)
	}
	if _, err := db.ExecContext(ctx, `WITH c AS (
 INSERT INTO pos_table_checks(outlet_id,table_number,actor_id,request_id,request_fingerprint,total_minor)
 VALUES(85,5,85,'00000000-0000-4000-8000-000000000099',decode(repeat('00',32),'hex'),1250)
 RETURNING id,outlet_id,table_number)
 INSERT INTO pos_table_check_slots(check_id,outlet_id,table_number) SELECT id,outlet_id,table_number FROM c`); err != nil {
		t.Fatal(err)
	}
	newID := findCheck(5)
	if retry := call("finalize", m, 85, 5, body); retry.err != nil || !reflect.DeepEqual(retry.body, x.body) || findCheck(5) != newID {
		t.Fatalf("old retry did not preserve replacement: %+v", retry)
	}
	fail(call("write", m, 85, 5, record(newID, 46, 500, "efectivo")), posTableTenderConflict)
	if got := call("legacy", m, 85, 5, legacy(49, "")); got.err != nil || got.status != 201 || got.body.(posSale).PaymentMethod != "efectivo" {
		t.Fatalf("older offline cash contract: %+v", got)
	}

}
