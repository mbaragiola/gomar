package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func TestPostgresMCPPayableMovementsMatchNativeAndDenyForeignLinks(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic-payable-history-fixture"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'mcp-payable-history',$1,TRUE,FALSE,FALSE)`, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Other','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',TRUE),(2,1,'Norte',TRUE),(3,2,'Other Centro',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'accounting',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'private-vendor','00000000003',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,
 currency,total_minor,description,status,revision,created_at,updated_at)
 SELECT id,1,CASE WHEN id=3 THEN 2 ELSE 1 END,1,'00000000003','invoice','A',1,id,'2026-11-01','ARS',10000,'private-document','reviewed',1,
 '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z' FROM generate_series(1,9) id;
 `)
	now := time.Date(2026, 11, 3, 2, 59, 59, 0, time.UTC)
	a := app{db: db, host: "example.com", enableMCP: true, payablesClock: func() time.Time { return now }}
	mux := a.routes()
	cookies := postgresTestCookies(t, mux, "mcp-payable-history", pass)
	ask := func(method, path, body, token string, want int, dst any) string {
		t.Helper()
		req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token == "" {
			for _, cookie := range cookies {
				req.AddCookie(cookie)
				if cookie.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", cookie.Value)
				}
			}
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want || (strings.HasPrefix(path, "/api/v1/mcp/") && w.Header().Get("Cache-Control") != "no-store") {
			t.Fatalf("%s %s status=%d want=%d: %s", method, path, w.Code, want, w.Body.String())
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
		return w.Body.String()
	}
	native := func(method, path, body string, want int, dst any) {
		t.Helper()
		ask(method, "/api/v1/entities/1"+path, body, "", want, dst)
	}
	for i := 1; i <= 9; i++ {
		var p payableDetailView
		native("POST", fmt.Sprintf("/documents/%d/payable", i), `{"motivo":"private-post-reason"}`, 201, &p)
		if p.ID != int64(i) {
			t.Fatal("unexpected synthetic payable ID")
		}
	}
	issue := func(body string) string {
		t.Helper()
		var data struct{ Token string }
		ask("POST", "/api/v1/auth/mcp-token", body, "", 201, &data)
		if len(data.Token) != 43 {
			t.Fatal("cannot issue fixture token")
		}
		return data.Token
	}
	key, broad := issue(`{"entity_id":1,"outlet_id":1}`), issue(`{"entity_id":1}`)
	read := func(outlet, payable int, query, token string, want int, dst any) string {
		t.Helper()
		wire := ask("GET", fmt.Sprintf("/api/v1/mcp/entities/1/outlets/%d/payable-records/%d/movements%s", outlet, payable, query), "", token, want, dst)
		for _, forbidden := range []string{"private-", "00000000003", "actor", "reason", "payment_id", "receipt", "supplier", "allocation", "reference", "reversal_of"} {
			if strings.Contains(wire, forbidden) {
				t.Fatalf("unexpected disclosure: %s", forbidden)
			}
		}
		return wire
	}
	get := func(query string) mcpclient.PayableMovements {
		t.Helper()
		var result mcpclient.PayableMovements
		read(1, 1, query, key, 200, &result)
		return result
	}
	wantMovement := func(row mcpclient.PayableMovement, kind, day string, delta, balance int64) {
		t.Helper()
		if row.Kind != kind || row.Date == nil || *row.Date != day || row.DeltaMinor != delta || row.BalanceMinor != balance {
			t.Fatalf("wrong movement: %+v", row)
		}
	}
	initial := read(1, 1, "", key, 200, nil)
	if strings.TrimSpace(initial) != `{"payable_id":1,"currency":"ARS","movements":[{"kind":"post","date":"2026-11-02","delta_minor":10000,"balance_minor":10000}]}` {
		t.Fatal("unexpected projected first page shape")
	}
	before := get("")
	if before.PayableID != 1 || before.Currency != "ARS" || len(before.Movements) != 1 || before.NextCursor != "" {
		t.Fatal("initial payable history mismatch")
	}
	wantMovement(before.Movements[0], "post", "2026-11-02", 10000, 10000)
	var draft paymentDetailView
	const draftBody = `{"outlet_id":1,"supplier_id":1,"moneda":"ARS","total":"42.00","metodo":"cash","referencia":"private-reference"}`
	native("POST", "/payments", draftBody, 201, &draft)
	paymentPath := "/payments/" + strconv.FormatInt(draft.ID, 10)
	if len(get("").Movements) != 1 {
		t.Fatal("an unapplied draft was projected")
	}
	now = now.Add(time.Minute)
	native("POST", paymentPath+"/post", fmt.Sprintf(`{"idempotency_key":%q,"aplicaciones":[{"payable_id":1,"importe":"12.00"},{"payable_id":2,"importe":"30.00"}]}`, draft.Key), 200, nil)
	posted := get("")
	if len(posted.Movements) != 2 {
		t.Fatal("split payment missing")
	}
	wantMovement(posted.Movements[1], "payment", "2026-11-03", -1200, 8800)
	native("POST", paymentPath+"/reverse", `{"motivo":"private-reversal-reason"}`, 201, nil)
	reversed := get("")
	if len(reversed.Movements) != 3 {
		t.Fatal("reversal missing")
	}
	wantMovement(reversed.Movements[2], "reversal", "2026-11-03", 1200, 10000)
	var nativeDetail payableDetailView
	native("GET", "/payables/1", "", 200, &nativeDetail)
	if nativeDetail.Balance != "100.00" {
		t.Fatal("native detail and MCP balance differ")
	}

	// These synthetic rows exercise legacy-only relationship shapes not normally
	// created by native writes: foreign allocation-only, ledger-only, forward
	// or reverse payment edge, and mismatched allocation reversal.
	exec(`
 INSERT INTO ap_payments(id,entity_id,outlet_id,supplier_id,currency,total_minor,method,reference,status,kind,reversal_of_id,
 reversal_reason,created_by_id,created_at,posted_by_id,posted_at,post_key) VALUES
 (10,1,2,1,'ARS',10,'cash','private-original','reversed','payment',NULL,'',1,'2026-11-03T03:00:00Z',1,'2026-11-03T03:00:00Z','legacy-10'),
 (11,1,1,1,'ARS',10,'cash','private-local','posted','reversal',10,'private-restore',1,'2026-11-03T03:00:00Z',1,'2026-11-03T03:00:00Z','legacy-11'),
 (12,1,1,1,'ARS',10,'cash','private-original','reversed','payment',NULL,'',1,'2026-11-03T03:00:00Z',1,'2026-11-03T03:00:00Z','legacy-12'),
 (13,1,2,1,'ARS',10,'cash','private-foreign','posted','reversal',12,'private-restore',1,'2026-11-03T03:00:00Z',1,'2026-11-03T03:00:00Z','legacy-13'),
 (14,1,1,1,'ARS',10,'cash','private-original','reversed','payment',NULL,'',1,'2026-11-03T03:00:00Z',1,'2026-11-03T03:00:00Z','legacy-14'),
 (15,1,1,1,'ARS',10,'cash','private-local','posted','reversal',14,'private-restore',1,'2026-11-03T03:00:00Z',1,'2026-11-03T03:00:00Z','legacy-15');
 INSERT INTO ap_allocations(id,payment_id,payable_id,amount_minor,created_at) VALUES
 (20,10,4,1,'2026-11-03T03:00:00Z'),(21,14,3,1,'2026-11-03T03:00:00Z');
 INSERT INTO ap_allocations(id,payment_id,payable_id,amount_minor,reverses_id,created_at)
 VALUES(22,15,8,1,21,'2026-11-03T03:00:00Z');
 INSERT INTO ap_ledger(payable_id,payment_id,kind,delta_minor,balance_minor,actor_id,reason,occurred_at)
 VALUES (5,10,'payment',-1,9999,1,'private-other','2026-11-03T03:00:00Z'),
 (6,11,'reversal',1,10000,1,'private-backlink','2026-11-03T03:00:00Z'),
 (7,12,'payment',-1,9999,1,'private-forwardlink','2026-11-03T03:00:00Z');
 `)
	for id := 4; id <= 8; id++ {
		read(1, id, "", broad, 404, nil)
	}
	read(1, 3, "", broad, 404, nil)
	read(2, 3, "", key, 404, nil)
	read(1, 999, "", broad, 404, nil)
	// Ledger order and row-based cursors survive equal/backdated timestamps, and
	// private legacy text used as a timestamp is discarded rather than echoed.
	exec(`
 INSERT INTO ap_ledger(payable_id,payment_id,kind,delta_minor,balance_minor,actor_id,reason,occurred_at)
 SELECT 1,1,'payment',-1,10000-i,1,'private-filler',
 CASE WHEN i=29 THEN 'private-invalid-date' WHEN i=27 THEN '2026-10-01T05:00:00-03:00' ELSE '2026-11-03T03:00:00Z' END
 FROM generate_series(1,30) i;
 `)
	first := get("")
	if len(first.Movements) != 30 || first.NextCursor == "" {
		t.Fatal("expected full first page with cursor")
	}
	wantMovement(first.Movements[29], "payment", "2026-10-01", -1, 9973)
	second := get("?cursor=" + first.NextCursor)
	if len(second.Movements) != 3 || second.NextCursor != "" || second.Movements[1].Date != nil {
		t.Fatal("expected remaining three movements with legacy date omitted")
	}
	wantMovement(second.Movements[0], "payment", "2026-11-03", -1, 9972)
	wantMovement(second.Movements[2], "payment", "2026-11-03", -1, 9970)
	for _, q := range []string{"?cursor=", "?cursor=1.0", "?cursor=1.01", "?cursor=1.%2b1", "?cursor=1.99999", "?cursor=1.2", "?cursor=1.1&cursor=1.1", "?after_id=1"} {
		read(1, 1, q, key, 422, nil)
	}
	read(1, 2, "?cursor="+first.NextCursor, key, 422, nil)
	read(1, 2, "?cursor=2."+strings.SplitN(first.NextCursor, ".", 2)[1], key, 422, nil)
	read(1, 9, "", key, 200, nil)
	var foreign paymentDetailView
	native("POST", "/payments", strings.Replace(draftBody, `"outlet_id":1`, `"outlet_id":2`, 1), 201, &foreign)
	foreignPath := "/payments/" + strconv.FormatInt(foreign.ID, 10)
	native("POST", foreignPath+"/post", fmt.Sprintf(`{"idempotency_key":%q,"aplicaciones":[{"payable_id":9,"importe":"42.00"}]}`, foreign.Key), 200, nil)
	read(1, 9, "", key, 404, nil)
	native("POST", foreignPath+"/reverse", `{"motivo":"private-foreign-reversal"}`, 201, nil)
	read(1, 9, "", key, 404, nil)
	ask("GET", "/api/v1/mcp/entities/2/outlets/3/payable-records/1/movements", "", broad, 404, nil)
	exec(`DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1`)
	read(1, 1, "", key, 404, nil)
	read(1, 1, "", broad, 404, nil)
	exec(`INSERT INTO outlet_grants VALUES(1,1); UPDATE outlets SET active=false WHERE id=1;`)
	read(1, 1, "", broad, 404, nil)
	exec(`UPDATE outlets SET active=true WHERE id=1; UPDATE memberships SET role='read_only' WHERE id=1;`)
	read(1, 1, "", key, 403, nil)
	read(1, 1, "", broad, 403, nil)
}
