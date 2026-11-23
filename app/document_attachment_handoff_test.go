package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresDraftAttachmentHandoffBrowserScope(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic-attachment-handoff-only"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'handoff-purchasing',$1,TRUE,FALSE,FALSE),(2,'handoff-accounting',$1,TRUE,FALSE,FALSE),
 (3,'handoff-reader',$1,TRUE,FALSE,FALSE),(4,'handoff-other',$1,TRUE,FALSE,FALSE)`, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Test entity','00000000000',TRUE),(2,'Other entity','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Test branch',TRUE),(2,1,'Sibling branch',TRUE),(3,2,'Other branch',TRUE),(4,1,'Inactive branch',FALSE);
 INSERT INTO memberships(id,user_id,entity_id,role,active)
 VALUES(1,1,1,'purchasing',TRUE),(2,2,1,'accounting',TRUE),(3,3,1,'read_only',TRUE),(4,4,2,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1),(3,1),(4,3),(1,4);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Synthetic supplier','30711111111',TRUE),(2,2,'Other supplier','30711111111',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at,deleted_at)
 VALUES(1,1,1,1,'30711111111','invoice','A',1,1,'2026-11-22','ARS',999,'Synthetic','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z',NULL),
 (2,1,2,1,'30711111111','invoice','A',1,2,'2026-11-22','ARS',999,'Synthetic','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z',NULL),
 (3,2,3,2,'30711111111','invoice','A',1,3,'2026-11-22','ARS',999,'Synthetic','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z',NULL),
 (4,1,4,1,'30711111111','invoice','A',1,4,'2026-11-22','ARS',999,'Synthetic','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z',NULL),
 (5,1,1,1,'30711111111','invoice','A',1,5,'2026-11-22','ARS',999,'Synthetic','reviewed',2,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z',NULL),
 (6,1,1,1,'30711111111','invoice','A',1,6,'2026-11-22','ARS',999,'Synthetic','draft',2,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z','2026-11-23T12:00:00Z');
 INSERT INTO document_attachments(document_id,storage_name,original_filename,mime_type,size,created_by_id,created_at)
 VALUES(1,'document-00000000000000000000000000000001','synthetic-only.png','image/png',42,1,'2026-11-22T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	h := (&app{db: db, host: "example.com", enableMCP: true}).routes()
	buyer := postgresTestCookies(t, h, "handoff-purchasing", pass)
	accounting := postgresTestCookies(t, h, "handoff-accounting", pass)
	reader := postgresTestCookies(t, h, "handoff-reader", pass)
	other := postgresTestCookies(t, h, "handoff-other", pass)
	call := func(path, bearer string, cookies []*http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://example.com"+path, nil)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status=%d cache=%q want=%d", path, w.Code, w.Header().Get("Cache-Control"), want)
		}
		return w
	}
	const route = "/api/v1/entities/1/outlets/1/documents/1/attachment-handoff"
	for _, login := range [][]*http.Cookie{buyer, accounting} {
		w := call(route, "", login, 200)
		var v documentView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.ID != 1 || v.Outlet.ID != 1 ||
			len(v.Attachments) != 1 || v.Attachments[0].Name != "synthetic-only.png" ||
			strings.Contains(w.Body.String(), "document-00000000000000000000000000000001") {
			t.Fatal("invalid browser-only draft metadata", err)
		}
	}
	call(route, "", reader, 403)
	call(route, "", other, 404)
	call(route, "", nil, 401)
	call(route, "synthetic-does-not-open-browser", nil, 401)
	call(route, "synthetic-does-not-open-browser", buyer, 422)
	call(route+"?outlet=2", "", buyer, 422)
	call(strings.Replace(route, "/outlets/1/", "/outlets/2/", 1), "", buyer, 404)
	call(strings.Replace(route, "/outlets/1/documents/1/", "/outlets/4/documents/4/", 1), "", buyer, 404)
	call(strings.Replace(route, "/documents/1/", "/documents/01/", 1), "", buyer, 404)
	call(strings.Replace(route, "/documents/1/", "/documents/5/", 1), "", buyer, 404)
	call(strings.Replace(route, "/documents/1/", "/documents/6/", 1), "", buyer, 404)
	// Ordinary native document detail remains available to the authorized
	// read-only role and to a user independently granted the sibling branch.
	call("/api/v1/entities/1/documents/1", "", reader, 200)
	call("/api/v1/entities/1/documents/2", "", buyer, 200)
	if _, err := db.ExecContext(ctx, "DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1"); err != nil {
		t.Fatal(err)
	}
	call(route, "", buyer, 404)
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET active=FALSE WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	call(route, "", accounting, 404)
}
