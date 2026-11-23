package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func TestPostgresMCPFinanceStockDetailsKeepBranchSettlementAndPrivacy(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic-finance-stock-fixture"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'finance-stock-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	const fixture = `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Other','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',TRUE),(2,1,'Norte',TRUE),(3,2,'Centro',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'private-supplier-sentinel','00000000003',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,
 currency,total_minor,description,status,revision,created_at,updated_at)
 SELECT id,1,CASE WHEN id=3 THEN 2 ELSE 1 END,1,'00000000003','invoice','A',1,id,'2026-11-01','ARS',1000,'private-document-sentinel','reviewed',1,
 '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z' FROM generate_series(1,34) id;
 INSERT INTO ap_payables(id,entity_id,outlet_id,supplier_id,source_document_id,currency,original_minor,balance_minor,supplier_name_snapshot,
 supplier_cuit_snapshot,document_reference_snapshot,issue_date_snapshot,due_date_snapshot,posted_by_id,posted_at)
 SELECT id,1,CASE WHEN id=3 THEN 2 ELSE 1 END,1,id,'ARS',1000,CASE WHEN id IN(1,2,4) THEN 700 ELSE 1000 END,
 'private-supplier-sentinel','00000000003','private-source-sentinel','2026-11-01','2026-11-25',1,'2026-11-02T02:59:59.000001Z'
 FROM generate_series(1,34) id;
 INSERT INTO ap_payments(id,entity_id,outlet_id,supplier_id,currency,total_minor,method,reference,status,kind,reversal_of_id,
 reversal_reason,created_by_id,created_at,posted_by_id,posted_at,post_key) VALUES
 (1,1,1,1,'ARS',300,'bank_transfer','private-reference-sentinel','posted','payment',NULL,'',1,'2026-11-03T02:00:00Z',1,'2026-11-03T03:00:00Z','private-key-1'),
 (2,1,2,1,'ARS',300,'bank_transfer','private-reference-sentinel','posted','payment',NULL,'',1,'2026-11-03T02:00:00Z',1,'2026-11-03T03:00:00Z','private-key-2'),
 (3,1,1,1,'ARS',600,'bank_transfer','private-reference-sentinel','reversed','payment',NULL,'',1,'2026-11-03T02:00:00Z',1,'2026-11-03T03:00:00Z','private-key-3'),
 (4,1,1,1,'ARS',600,'bank_transfer','private-reference-sentinel','posted','reversal',3,'private-reason-sentinel',1,'2026-11-03T02:00:00Z',1,'2026-11-03T03:00:00Z','private-key-4'),
 (5,1,1,1,'USD',155,'cash','private-reference-sentinel','draft','payment',NULL,'',1,'2026-11-03T06:00:00Z',NULL,NULL,'private-key-5'),
 (6,1,1,1,'ARS',10,'bank_transfer','private-reference-sentinel','reversed','payment',NULL,'',1,'2026-11-03T02:00:00Z',1,'2026-11-03T03:00:00Z','private-key-6'),
 (7,1,1,1,'ARS',10,'bank_transfer','private-reference-sentinel','posted','reversal',6,'private-reason-sentinel',1,'2026-11-03T02:00:00Z',1,'2026-11-03T03:00:00Z','private-key-7');
 INSERT INTO ap_allocations(payment_id,payable_id,amount_minor,created_at) VALUES
 (1,1,300,'2026-11-03T03:00:00Z'),(2,2,300,'2026-11-03T03:00:00Z'),(3,3,300,'2026-11-03T03:00:00Z');
 INSERT INTO ap_ledger(payable_id,payment_id,kind,delta_minor,balance_minor,actor_id,reason,occurred_at)
 VALUES(4,2,'payment',-300,700,1,'private-ledger-sentinel','2026-11-03T03:00:00Z');
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1),(2,2,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
 VALUES(1,1,1,'Stock sample',50,TRUE),(2,2,1,'Other stock',50,TRUE);
 INSERT INTO stock_outlets VALUES(1,TRUE),(2,TRUE);
 INSERT INTO stock_products VALUES(1,1,TRUE,1),(2,2,TRUE,1);
 INSERT INTO stock_movements(id,outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
 VALUES(1,1,1,1,'recepcion',9,9,'2026-11-03T02:59:59.999Z','11111111-0000-4000-8000-000000000001'),
 (2,1,1,1,'recepcion',2,2,'2026-11-03T03:00:00.001Z','11111111-0000-4000-8000-000000000002'),
 (3,1,1,1,'merma',1,-1,'2026-11-03T12:00:00Z','11111111-0000-4000-8000-000000000003'),
 (4,1,1,1,'recepcion',1,1,'2026-11-04T03:00:00.001Z','11111111-0000-4000-8000-000000000004');
 INSERT INTO stock_movements(id,outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
 SELECT i,1,1,1,'recepcion',1,1,'2026-11-03T16:00:00Z','22222222-0000-4000-8000-'||lpad(i::text,12,'0')
 FROM generate_series(5,34) AS i;
 `
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableStock: true}
	mux := a.routes()
	cookies := postgresTestCookies(t, mux, "finance-stock-fixture", pass)
	ask := func(method, path, body, key string, login bool, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if login {
			for _, c := range cookies {
				req.AddCookie(c)
				if c.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d want=%d", path, w.Code, want)
		}
		return w.Body.String()
	}
	issue := func(body string) string {
		t.Helper()
		var k struct{ Token string }
		if json.Unmarshal([]byte(ask("POST", "/api/v1/auth/mcp-token", body, "", true, 201)), &k) != nil || len(k.Token) != 43 {
			t.Fatal("test key unavailable")
		}
		return k.Token
	}
	broad := issue(`{"entity_id":1}`)
	narrow := issue(`{"entity_id":1,"outlet_id":1}`)
	const base = "/api/v1/mcp/entities/1/outlets/1"
	const stock = "/stock/products/1/movements?days=1&start_date=2026-11-03"
	read := func(path, key string, want int, dst any) string {
		t.Helper()
		output := ask("GET", base+path, "", key, false, want)
		if dst != nil {
			if err := json.Unmarshal([]byte(output), dst); err != nil {
				t.Fatal(err)
			}
		}
		for _, secret := range []string{"private-", "00000000003", "actor", "supplier", "method", "reason", "receipt", "allocations", "payment_id", "order", "reversal_of", "nombre"} {
			if strings.Contains(output, secret) {
				t.Fatalf("%s leaked field/value %s", path, secret)
			}
		}
		return output
	}
	var pay mcpclient.PayableRecord
	read("/payable-records/1", narrow, 200, &pay)
	if pay.Status != "partial" || pay.PostedDate != "2026-11-01" || pay.TotalMinor != 1000 || pay.BalanceMinor != 700 {
		t.Fatal("incorrect payable data")
	}
	for _, path := range []string{"/payable-records/2", "/payable-records/3", "/payable-records/4", "/recorded-payments/2", "/recorded-payments/3", "/recorded-payments/4"} {
		read(path, broad, 404, nil)
	}
	var list mcpclient.FinanceRecords[mcpclient.PayableRecord]
	read("/payable-records", broad, 200, &list)
	if len(list.Records) != 30 || list.Records[0].ID != 1 || list.Records[1].ID != 5 || list.NextID != 33 {
		t.Fatal("incorrect first payable page")
	}
	list = mcpclient.FinanceRecords[mcpclient.PayableRecord]{}
	read("/payable-records?after_id=33", broad, 200, &list)
	if len(list.Records) != 1 || list.Records[0].ID != 34 || list.NextID != 0 {
		t.Fatal("incorrect last payable page")
	}
	var p mcpclient.RecordedPayment
	read("/recorded-payments/1", narrow, 200, &p)
	if p.Status != "posted" || p.CreatedDate != "2026-11-02" || p.PostedDate == nil || *p.PostedDate != "2026-11-03" {
		t.Fatal("payment dates were not local")
	}
	var payments mcpclient.FinanceRecords[mcpclient.RecordedPayment]
	read("/recorded-payments", broad, 200, &payments)
	if len(payments.Records) != 4 || payments.Records[1].ID != 5 || payments.Records[1].PostedDate != nil ||
		payments.Records[3].Status != "posted_reversal" {
		t.Fatal("unexpected projected records")
	}
	var moves mcpclient.StockMovementDetails
	out := read(stock, narrow, 200, &moves)
	if strings.Contains(out, `"id"`) || strings.Contains(out, "22222222") || moves.NextID != 32 || len(moves.Movements) != 30 ||
		moves.Movements[0].RecordedAt != "2026-11-03T00:00:00.001-03:00" || moves.Movements[1].Delta != -1 {
		t.Fatal("incorrect stock cursor, dates, or privacy")
	}
	moves = mcpclient.StockMovementDetails{}
	read(stock+"&after_id=32", narrow, 200, &moves)
	if len(moves.Movements) != 2 || moves.NextID != 0 {
		t.Fatal("incorrect last stock page")
	}
	read("/stock/products/2/movements?days=1&start_date=2026-11-03", broad, 404, nil)
	read(stock+"&days=2", broad, 422, nil)
	read("/stock/products/1/movements?days=32&start_date=2026-11-03", broad, 422, nil)
	ask("GET", "/api/v1/mcp/entities/2/outlets/3/payable-records", "", broad, false, 404)
	ask("GET", "/api/v1/mcp/entities/1/outlets/2/recorded-payments", "", narrow, false, 404)
	// Native dates are immutable snapshots, so insert new synthetic records
	// after asserting the legacy unfiltered pages above.
	const dueFixture = `
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,
 currency,total_minor,description,status,revision,created_at,updated_at)
 SELECT id,1,CASE WHEN id=70 THEN 2 ELSE 1 END,1,'00000000003','invoice','A',1,id,'2026-11-01','ARS',1000,'private-document-sentinel','reviewed',1,
 '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z' FROM generate_series(35,70) id;
 INSERT INTO ap_payables(id,entity_id,outlet_id,supplier_id,source_document_id,currency,original_minor,balance_minor,supplier_name_snapshot,
 supplier_cuit_snapshot,document_reference_snapshot,issue_date_snapshot,due_date_snapshot,posted_by_id,posted_at)
 SELECT id,1,CASE WHEN id=70 THEN 2 ELSE 1 END,1,id,'ARS',1000,1000,'private-supplier-sentinel','00000000003','private-source-sentinel',
 '2026-11-01',CASE id WHEN 35 THEN NULL WHEN 36 THEN '2026-12-01' WHEN 37 THEN '2026-12-02'
 WHEN 68 THEN '2026-12-04' WHEN 69 THEN '2026-12-05' ELSE '2026-12-03' END,1,'2026-11-02T02:59:59Z'
 FROM generate_series(35,70) id;
 `
	if _, err := db.ExecContext(ctx, dueFixture); err != nil {
		t.Fatal(err)
	}
	const listPath = "/payable-records"
	const bounds = "?due_from=2026-12-02&due_to=2026-12-04"
	list = mcpclient.FinanceRecords[mcpclient.PayableRecord]{}
	read(listPath+bounds, narrow, 200, &list)
	if len(list.Records) != 30 || list.NextID != 66 {
		t.Fatal("date filter must run before LIMIT and cursor calculation")
	}
	for i, item := range list.Records {
		if item.ID != int64(i+37) || item.DueDate == nil {
			t.Fatal("filtered first page scope/order")
		}
	}
	if *list.Records[0].DueDate != "2026-12-02" {
		t.Fatal("lower boundary must be inclusive")
	}
	list = mcpclient.FinanceRecords[mcpclient.PayableRecord]{}
	read(listPath+bounds+"&after_id=66", narrow, 200, &list)
	if len(list.Records) != 2 || list.NextID != 0 || list.Records[0].ID != 67 || list.Records[1].ID != 68 || *list.Records[1].DueDate != "2026-12-04" {
		t.Fatal("upper boundary, foreign outlet and filtered last page")
	}
	list = mcpclient.FinanceRecords[mcpclient.PayableRecord]{}
	read(listPath+"?after_id=34", narrow, 200, &list)
	if len(list.Records) != 30 || list.Records[0].ID != 35 || list.Records[0].DueDate != nil {
		t.Fatal("unfiltered page no longer includes the undated payable")
	}
	list = mcpclient.FinanceRecords[mcpclient.PayableRecord]{}
	read(listPath+"?due_from=2026-11-25&due_to=2026-11-25", narrow, 200, &list)
	if len(list.Records) != 30 || list.Records[0].ID != 1 || list.Records[1].ID != 5 {
		t.Fatal("same-day filter changed prior cross-outlet settlement exclusions")
	}
	for _, suffix := range []string{
		"?due_from=2026-12-02", "?due_to=2026-12-04", "?due_from=&due_to=", "?due_from=0000-12-02&due_to=2026-12-04",
		"?due_from=2026-11-31&due_to=2026-12-04", "?due_from=2026-12-2&due_to=2026-12-04", "?due_from=2026-12-05&due_to=2026-12-04",
		bounds + "&due_from=2026-12-02", bounds + "&due_to=2026-12-04", bounds + "&after_id=0", bounds + "&after_id=1&after_id=2", bounds + "&currency=ARS",
	} {
		read(listPath+suffix, narrow, 422, nil)
	}
	read("/recorded-payments"+bounds, narrow, 422, nil)
	ask("GET", "/api/v1/mcp/entities/1/outlets/2/payable-records"+bounds, "", narrow, false, 404)
	ask("GET", "/api/v1/mcp/entities/2/outlets/3/payable-records"+bounds, "", broad, false, 404)
	if _, err := db.ExecContext(ctx, `DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1`); err != nil {
		t.Fatal(err)
	}
	read(listPath+bounds, narrow, 404, nil)
	if _, err := db.ExecContext(ctx, `INSERT INTO outlet_grants VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='read_only' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	read("/payable-records", narrow, 403, nil)
	read(listPath+bounds, narrow, 403, nil)
	read("/recorded-payments", narrow, 403, nil)
	read(stock, narrow, 403, nil)
}
