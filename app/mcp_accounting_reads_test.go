package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func TestMCPAccountingCursor(t *testing.T) {
	for _, q := range []string{"after_id=0", "after_id=-1", "after_id=over", "after_id=1&after_id=2", "offset=1", "after_id=", "after_id=9223372036854775808", "after_id=%"} {
		if _, err := mcpAccountingCursor(httptest.NewRequest("GET", "/?"+q, nil)); err != badInput {
			t.Errorf("%q: expected invalid cursor", q)
		}
	}
	for q, want := range map[string]int64{"": 0, "?after_id=2": 2} {
		if got, err := mcpAccountingCursor(httptest.NewRequest("GET", "/"+q, nil)); err != nil || got != want {
			t.Errorf("%q: got %d, %v", q, got, err)
		}
	}
}

func TestPostgresMCPAccountingReadsScopeAndProjection(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic-accounting-fixture"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
		VALUES(1,'accounting-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	fixture := `
	INSERT INTO entities(id,name,cuit,active) VALUES(1,'First','00000000001',TRUE),(2,'Second','00000000002',TRUE);
	INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Second',TRUE),
		(3,1,'Ungranted',TRUE),(4,2,'Foreign',TRUE),(5,1,'Inactive',FALSE);
	INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
	INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,5);
	INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Fixture supplier','00000000003',TRUE),
		(2,1,'inactive supplier sentinel','00000000004',FALSE),(3,2,'foreign supplier sentinel','00000000005',TRUE);
	INSERT INTO suppliers(id,entity_id,name,cuit,active) SELECT i,1,'Synthetic '||i,'0000000'||(1000+i),TRUE FROM generate_series(4,34) i;
	INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,due_date,
		currency,total_minor,description,status,revision,created_at,updated_at,deleted_at)
	SELECT i,1,1,1,'00000000003','invoice','A',1,i,'2026-11-01',NULL,'ARS',25,'private note sentinel','draft',1,'time','time',NULL
	FROM generate_series(1,31) i;
	INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,due_date,
		currency,total_minor,description,status,revision,created_at,updated_at,deleted_at) VALUES
		(32,1,1,1,'00000000003','invoice','A',1,32,'2026-11-01',NULL,'ARS',25,'deleted sentinel','draft',1,'time','time','deleted'),
		(33,1,2,1,'00000000003','invoice','A',1,33,'2026-11-01',NULL,'ARS',25,'other branch sentinel','draft',1,'time','time',NULL);
	INSERT INTO ap_payments(id,entity_id,outlet_id,supplier_id,currency,total_minor,method,reference,status,kind,reversal_of_id,
		reversal_reason,created_by_id,created_at,posted_by_id,posted_at,post_key) VALUES
		(1,1,1,1,'ARS',50,'cash','secret reference','draft','payment',NULL,'',1,'time',NULL,NULL,'draft'),
		(2,1,1,1,'ARS',50,'cash','secret reference','posted','payment',NULL,'',1,'time',1,'time','posted'),
		(3,1,1,1,'ARS',50,'cash','secret reference','reversed','payment',NULL,'',1,'time',1,'time','reversed'),
		(4,1,1,1,'ARS',50,'cash','secret reference','posted','reversal',3,'test reason',1,'time',1,'time','reversal'),
		(5,1,2,1,'ARS',50,'cash','other branch','draft','payment',NULL,'',1,'time',NULL,NULL,'other');
	INSERT INTO reconciliation_read_runs(id,entity_id,state,revision,rules_version,match_reference,created_at) VALUES
		(1,1,'draft',1,1,FALSE,'time'),(2,1,'review',1,1,FALSE,'time'),(3,1,'closed',1,1,FALSE,'time'),
		(4,1,'draft',1,1,FALSE,'time'),(5,1,'deleted',1,1,FALSE,'time'),(6,1,'draft',1,1,FALSE,'time'),(7,2,'draft',1,1,FALSE,'time'),
		(8,1,'review',1,1,FALSE,'time');
	INSERT INTO reconciliation_read_outlets VALUES(1,1),(2,1),(2,2),(3,1),(4,1),(4,3),(5,1),(6,2),(7,4),(8,1),(8,5);`
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}

	a := &app{db: db, host: "example.com", enableMCP: true}
	router := a.routes()
	cookies := postgresTestCookies(t, router, "accounting-fixture", pass)
	readMux := http.NewServeMux()
	a.registerMCPAccountingReads(func(path, method string, handler http.HandlerFunc) {
		readMux.HandleFunc(method+" "+path, handler)
	})
	issue := func(body string) string {
		t.Helper()
		r := httptest.NewRequest("POST", "http://example.com/api/v1/auth/mcp-token", strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
			if cookie.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var result struct{ Token string }
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Token) != 43 {
			t.Fatalf("synthetic token was not issued: %d", w.Code)
		}
		return result.Token
	}
	broad := issue(`{"entity_id":1}`)
	narrow := issue(`{"entity_id":1,"outlet_id":1}`)
	read := func(path, token string, want int, dst any) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://example.com/api/v1/mcp/entities/"+path, nil).WithContext(ctx)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		readMux.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: got %d, want %d with no-store", path, w.Code, want)
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
		for _, forbidden := range []string{"0000000000", "sentinel", "secret reference", "test reason", "\"receipt", "\"attachment", "\"sources", "\"method", "\"cuit", "storage_name"} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatalf("%s returned a forbidden field or value", path)
			}
		}
	}
	const base = "1/outlets/1"
	var docs mcpclient.DocumentList
	read(base+"/documents", narrow, 200, &docs)
	if len(docs.Documents) != 30 || docs.NextID != 30 || docs.Documents[0].ID != 1 {
		t.Fatal(docs)
	}
	docs = mcpclient.DocumentList{}
	read(base+"/documents?after_id=30", narrow, 200, &docs)
	if len(docs.Documents) != 1 || docs.NextID != 0 || docs.Documents[0].ID != 31 {
		t.Fatal(docs)
	}
	read(base+"/documents?after_id=30&x=1", narrow, 422, nil)
	if _, err := db.ExecContext(ctx, `INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,due_date,
  currency,total_minor,description,status,revision,created_at,updated_at,deleted_at) VALUES
  (40,1,1,1,'00000000003','debit_note','A',1,40,'2025-11-01',NULL,'ARS',99999999999999,'private sentinel','reviewed',1,'time','time',NULL),
  (41,1,1,1,'00000000003','credit_note','A',1,41,'2026-11-01',NULL,'ARS',99999999999999,'private sentinel','draft',1,'time','time',NULL),
  (42,1,1,2,'00000000004','credit_note','A',1,42,'2026-11-01',NULL,'USD',125,'private sentinel','reviewed',1,'time','time',NULL)`); err != nil {
		t.Fatal(err)
	}
	var totals mcpclient.DocumentTotals
	read(base+"/document-totals", narrow, 200, &totals)
	if !reflect.DeepEqual(totals, mcpclient.DocumentTotals{Count: 34, Totals: []mcpclient.DocumentCurrencyTotal{
		{Currency: "ARS", Count: 33, TotalMinor: "775"}, {Currency: "USD", Count: 1, TotalMinor: "-125"},
	}}) {
		t.Fatal(totals)
	}
	// Compare independently against the native browser read for the same outlet.
	nativeReq := httptest.NewRequest("GET", "http://example.com/api/v1/entities/1/totals?outlet_id=1", nil).WithContext(ctx)
	for _, cookie := range cookies {
		nativeReq.AddCookie(cookie)
	}
	native := httptest.NewRecorder()
	router.ServeHTTP(native, nativeReq)
	if native.Code != 200 || native.Body.String() != "{\"cantidad\":34,\"totales\":[{\"cantidad\":33,\"moneda\":\"ARS\",\"total\":\"7.75\"},{\"cantidad\":1,\"moneda\":\"USD\",\"total\":\"-1.25\"}]}\n" {
		t.Fatal(native.Code, native.Body.String())
	}
	read(base+"/document-totals?status=reviewed", narrow, 422, nil)
	read(base+"/document-totals?after_id=1", narrow, 422, nil)
	read("1/outlets/2/document-totals", broad, 200, &totals)
	if totals.Count != 1 || len(totals.Totals) != 1 || totals.Totals[0].TotalMinor != "25" {
		t.Fatal(totals)
	}
	if _, err := db.ExecContext(ctx, "UPDATE documents SET deleted_at='time' WHERE id=33"); err != nil {
		t.Fatal(err)
	}
	read("1/outlets/2/document-totals", broad, 200, &totals)
	if totals.Count != 0 || totals.Totals == nil || len(totals.Totals) != 0 {
		t.Fatal(totals)
	}
	var suppliers mcpclient.SupplierList
	read(base+"/suppliers", narrow, 200, &suppliers)
	if len(suppliers.Suppliers) != 30 || suppliers.Suppliers[0].ID != 1 || suppliers.Suppliers[1].ID != 4 || suppliers.NextID != 32 {
		t.Fatal(suppliers)
	}
	var pay mcpclient.PaymentStatusCounts
	read(base+"/payments-summary", narrow, 200, &pay)
	if pay.Draft != 1 || pay.Posted != 1 || pay.Reversed != 1 || pay.Reversals != 1 {
		t.Fatal(pay)
	}
	var rec mcpclient.ReconciliationStatusCounts
	read(base+"/reconciliation-summary", narrow, 200, &rec)
	if rec.Draft != 1 || rec.Review != 0 || rec.Closed != 1 {
		t.Fatal(rec)
	}
	read(base+"/reconciliation-summary", broad, 200, &rec)
	if rec.Draft != 1 || rec.Review != 1 || rec.Closed != 1 {
		t.Fatal(rec)
	}
	for _, endpoint := range []string{"/document-totals", "/documents", "/suppliers", "/payments-summary", "/reconciliation-summary"} {
		read(base+endpoint, "", 401, nil)
		read("1/outlets/2"+endpoint, narrow, 404, nil)
		read("1/outlets/3"+endpoint, broad, 404, nil)
		read("1/outlets/5"+endpoint, broad, 404, nil)
		read("2/outlets/4"+endpoint, broad, 404, nil)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=2"); err != nil {
		t.Fatal(err)
	}
	read(base+"/reconciliation-summary", broad, 200, &rec)
	if rec.Review != 0 {
		t.Fatal("reconciliation remained visible after losing grant")
	}
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='accounting' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	read(base+"/payments-summary", broad, 200, &pay)
	read(base+"/reconciliation-summary", broad, 403, nil)
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='read_only' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	read(base+"/documents", broad, 200, nil)
	read(base+"/document-totals", broad, 200, nil)
	read(base+"/suppliers", broad, 200, nil)
	read(base+"/payments-summary", broad, 403, nil)
}
