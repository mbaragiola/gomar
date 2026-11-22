package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func TestPostgresMCPDetailReadsRespectCurrentScopesAndProjectMetadata(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic mcp detail test"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'detail-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	const fixture = `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Other','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',TRUE),(2,1,'Norte',TRUE),(3,1,'Sin permiso',TRUE),(4,2,'Centro',TRUE),(5,1,'Inactivo',FALSE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,5);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'supplier-private-sentinel','00000000003',TRUE),(2,2,'foreign-supplier','00000000004',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,due_date,
  currency,subtotal_minor,tax_minor,total_minor,description,status,revision,created_at,updated_at,deleted_at) VALUES
  (1,1,1,1,'00000000003','invoice','A',2,42,'2026-11-01','2026-11-22','ARS',1000,210,1210,'comment-private-sentinel','draft',3,'time','time',NULL),
  (2,1,2,1,'00000000003','invoice','B',2,43,'2026-11-01',NULL,'ARS',NULL,NULL,50,'other-outlet','reviewed',1,'time','time',NULL),
  (3,1,1,1,'00000000003','invoice','B',2,44,'2026-11-01',NULL,'ARS',NULL,NULL,60,'deleted-row','reviewed',1,'time','time','deletion'),
  (4,2,4,2,'00000000004','invoice','B',2,45,'2026-11-01',NULL,'ARS',NULL,NULL,70,'other-entity','reviewed',1,'time','time',NULL),
  (5,1,1,1,'00000000003','credit_note','',0,1,'2026-11-01',NULL,'USD',NULL,NULL,0,'legacy revision','draft',0,'time','time',NULL);
 INSERT INTO reconciliation_read_runs(id,entity_id,state,revision,rules_version,match_reference,created_at,updated_at,closed_at,
   unresolved_count,confirmed_count,rejected_count) VALUES
  (1,1,'review',2,1,FALSE,'2026-11-05T00:00:00Z','2026-11-06T12:00:00Z',NULL,3,4,1),
  (2,1,'closed',7,1,FALSE,'2026-11-05T00:00:00Z',NULL,'2026-11-07T08:00:00Z',0,6,2),
  (3,1,'draft',1,1,FALSE,'2026-11-05T00:00:00Z',NULL,NULL,NULL,0,0),
  (4,1,'deleted',1,1,FALSE,'2026-11-05T00:00:00Z',NULL,NULL,NULL,0,0),
  (5,1,'review',1,1,FALSE,'2026-11-05T00:00:00Z',NULL,NULL,NULL,0,0),
  (6,2,'draft',1,1,FALSE,'2026-11-05T00:00:00Z',NULL,NULL,NULL,0,0),
  (7,1,'draft',1,1,FALSE,'2026-11-05T00:00:00Z',NULL,NULL,NULL,0,0);
 INSERT INTO reconciliation_read_outlets VALUES(1,1),(2,1),(2,2),(3,1),(3,3),(4,1),(5,1),(5,5),(6,4),(7,2);
 INSERT INTO reconciliation_read_sources(run_id,side,label,mapping,formats,default_currency,default_outlet_id,row_count)
 VALUES(1,'payments','csv-private-sentinel','{"hidden":"bank-account-sentinel"}','{}','ARS',1,4),
  (1,'cash_flow','other-source-private-sentinel','{}','{}','ARS',1,5);
 INSERT INTO reconciliation_read_runs(id,entity_id,state,revision,rules_version,match_reference,created_at)
 SELECT i,1,'draft',1,1,FALSE,'2026-11-05T00:00:00Z' FROM generate_series(100,129) i;
 INSERT INTO reconciliation_read_outlets SELECT i,1 FROM generate_series(100,129) i;`
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true}
	routes := a.routes()
	cookies := postgresTestCookies(t, routes, "detail-fixture", pass)
	ask := func(method, path, body, key string, login bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if login {
			for _, cookie := range cookies {
				req.AddCookie(cookie)
				if cookie.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", cookie.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, req)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s returned %d instead of %d, cache %q", path, w.Code, want, w.Header().Get("Cache-Control"))
		}
		return w
	}
	issue := func(body string) string {
		t.Helper()
		var k struct{ Token string }
		w := ask("POST", "/api/v1/auth/mcp-token", body, "", true, 201)
		if json.Unmarshal(w.Body.Bytes(), &k) != nil || len(k.Token) != 43 {
			t.Fatal("synthetic key not issued")
		}
		return k.Token
	}
	full := issue(`{"entity_id":1}`)
	narrow := issue(`{"entity_id":1,"outlet_id":1}`)
	const base = "/api/v1/mcp/entities/1/outlets/1"
	const runs = "/reconciliation-runs"
	read := func(path, key string, want int, out any) {
		t.Helper()
		w := ask("GET", base+path, "", key, false, want)
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		for _, private := range []string{"private-sentinel", "bank-account-sentinel", "00000000003", "attachments", "description", "supplier", "outlet_ids", "mapping", "default_currency", "audit"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("%s leaked a field: %s", path, private)
			}
		}
	}
	var doc mcpclient.DocumentDetails
	read("/documents/1", narrow, 200, &doc)
	if doc.Revision != 3 || doc.Status != "draft" || doc.Reference != "Factura A 00002-00000042" ||
		doc.NetMinor == nil || *doc.NetMinor != 1000 || doc.TaxMinor == nil || *doc.TaxMinor != 210 || doc.TotalMinor != 1210 || doc.DueDate == nil {
		t.Fatal("incorrect document projection")
	}
	doc = mcpclient.DocumentDetails{}
	read("/documents/5", narrow, 200, &doc)
	if doc.Revision != 0 || doc.TotalMinor != 0 || doc.DueDate != nil || doc.NetMinor != nil || doc.TaxMinor != nil {
		t.Fatal("legacy zero or absent fields changed")
	}
	for _, path := range []string{"/documents/2", "/documents/3", "/documents/4"} {
		read(path, narrow, 404, nil)
	}
	ask("GET", "/api/v1/mcp/entities/2/outlets/4/documents/4", "", full, false, 404)
	read("/documents/1?include=attachments", narrow, 422, nil)
	var run mcpclient.ReconciliationRun
	read(runs+"/1", narrow, 200, &run)
	if run.Revision != 2 || run.Counts.Outlets != 1 || run.Counts.Sources != 2 || run.Counts.Rows != 9 ||
		run.Counts.Confirmed != 4 || run.Counts.Unresolved == nil || *run.Counts.Unresolved != 3 || run.UpdatedAt == nil {
		t.Fatal("incorrect reconciliation counts")
	}
	read(runs+"/2", narrow, 404, nil)
	run = mcpclient.ReconciliationRun{}
	read(runs+"/2", full, 200, &run)
	if run.Status != "closed" || run.Counts.Outlets != 2 || run.Revision != 7 || run.ClosedAt == nil {
		t.Fatal("shared authorized run unavailable")
	}
	for _, path := range []string{"/3", "/4", "/5", "/6", "/7"} {
		read(runs+path, full, 404, nil)
	}
	var page mcpclient.ReconciliationRuns
	read(runs, full, 200, &page)
	if len(page.Runs) != 30 || page.Runs[0].ID != 1 || page.Runs[1].ID != 2 || page.NextID != 127 {
		t.Fatal("incorrect first reconciliation page")
	}
	page = mcpclient.ReconciliationRuns{}
	read(runs+"?after_id=127", full, 200, &page)
	if len(page.Runs) != 2 || page.Runs[0].ID != 128 || page.Runs[1].ID != 129 || page.NextID != 0 {
		t.Fatal("incorrect second reconciliation page")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=2`); err != nil {
		t.Fatal(err)
	}
	read(runs+"/2", full, 404, nil)
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='read_only' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	read(runs, full, 403, nil)
	read(runs+"/1", narrow, 403, nil)
	read("/documents/1", narrow, 200, nil)
}
