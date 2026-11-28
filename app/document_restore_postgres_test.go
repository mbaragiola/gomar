package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPostgresRestoreRemovedDraft(t *testing.T) {
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
	seed(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Main','30111111118',TRUE),(2,'Other','00000000002',TRUE);
	INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Target',TRUE),(2,1,'Granted elsewhere',TRUE),
		(3,1,'Ungranted',TRUE),(4,2,'Other tenant',TRUE),(5,1,'Archived',FALSE);
	INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'purchasing-fixture','!synthetic',TRUE,FALSE,FALSE),
		(2,'read-fixture','!synthetic',TRUE,FALSE,FALSE);
	INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'purchasing',TRUE),(2,2,1,'read_only',TRUE);
	INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,5),(2,1);
	INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Synthetic issuer','30711111111',TRUE);`)
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &app{db: db, host: "example.com", privateRoot: root, enableMCP: true}
	router := a.routes()
	manager := nativeDocumentSession(t, a, router, 1)
	reader := nativeDocumentSession(t, a, router, 2)
	call := func(r *http.Request, cookies []*http.Cookie, token string, want int) []byte {
		t.Helper()
		r = r.WithContext(ctx)
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", r.Method, r.URL.Path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	req := func(method, path, body string, cookies []*http.Cookie, token string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "http://example.com/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return call(r, cookies, token, want)
	}
	decode := func(data []byte, dst any) {
		t.Helper()
		if err := json.Unmarshal(data, dst); err != nil {
			t.Fatal(err)
		}
	}
	const docs = "/entities/1/documents"
	const removed = "/entities/1/outlets/1/removed-drafts"
	const mcpRemoved = "/mcp/entities/1/outlets/1/removed-drafts"
	const draft = `{"outlet_id":1,"supplier_id":1,"issuer_cuit":"30711111111","tipo":"invoice","letra":"A","punto_venta":3,"numero":40,"moneda":"ARS","fecha_emision":"2026-10-05","total":"121.12","descripcion":"private sentinel"}`
	var created struct{ ID int64 }
	decode(req("POST", docs, draft, manager, "", 201), &created)
	doc := fmt.Sprintf("%s/%d", docs, created.ID)
	mcpDoc := fmt.Sprintf("/mcp/entities/1/outlets/1/documents/%d", created.ID)
	restore := fmt.Sprintf("%s/%d/restore", removed, created.ID)
	var key struct{ Token string }
	decode(req("POST", "/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, manager, "", 201), &key)
	if key.Token == "" {
		t.Fatal("missing synthetic token")
	}
	png := nativeDocumentPNG(t)
	call(nativeDocumentUpload(t, "http://example.com/api/v1"+doc+"/attachments", "private-sentinel.png", png), manager, "", 201)
	var attachmentID, revision int64
	if err := db.QueryRowContext(ctx, `SELECT a.id,d.revision FROM documents d JOIN document_attachments a ON a.document_id=d.id WHERE d.id=$1`, created.ID).
		Scan(&attachmentID, &revision); err != nil {
		t.Fatal(err)
	}
	attachment := fmt.Sprintf("%s/attachments/%d", doc, attachmentID)
	req("DELETE", doc, fmt.Sprintf(`{"motivo":"Baja original","expected_revision":%d}`, revision), manager, "", 200)
	revision++
	body := fmt.Sprintf(`{"motivo":"Corrección operativa","expected_revision":%d}`, revision)
	req("GET", mcpDoc, "", nil, key.Token, 404)
	req("GET", doc, "", manager, "", 404)
	req("GET", attachment, "", manager, "", 404)
	req("POST", docs, draft, manager, "", 409)
	req("GET", removed, "", reader, "", 403)
	req("POST", restore, body, reader, "", 403)
	req("POST", restore, body, nil, key.Token, 403)
	for _, path := range []string{"/entities/1/outlets/2/removed-drafts", "/entities/1/outlets/3/removed-drafts",
		"/entities/1/outlets/5/removed-drafts", "/entities/2/outlets/4/removed-drafts"} {
		req("POST", fmt.Sprintf("%s/%d/restore", path, created.ID), body, manager, "", 404)
	}
	req("GET", removed+"?before_id=1&before_id=2", "", manager, "", 422)
	list := req("GET", removed, "", manager, "", 200)
	var page struct{ Items []removedDraft }
	decode(list, &page)
	if len(page.Items) != 1 || page.Items[0].ID != created.ID || page.Items[0].Revision != revision || bytes.Contains(list, []byte("private sentinel")) || bytes.Contains(list, []byte("private-sentinel.png")) {
		t.Fatalf("wrong removed draft list: %s", list)
	}
	target := fmt.Sprintf("?draft_id=%d", created.ID)
	req("GET", removed+target, "", manager, "", 200)
	req("GET", removed+target, "", reader, "", 403)
	req("GET", "/entities/1/outlets/2/removed-drafts"+target, "", manager, "", 404)
	mcpList := req("GET", mcpRemoved, "", nil, key.Token, 200)
	wantHandoff := fmt.Sprintf("/#restaurar-borrador=1:1:%d", created.ID)
	if !bytes.Contains(mcpList, []byte(wantHandoff)) || bytes.Contains(mcpList, []byte("issuer_cuit")) ||
		bytes.Contains(mcpList, []byte("private-sentinel.png")) {
		t.Fatalf("wrong MCP handoff: %s", mcpList)
	}
	req("GET", "/mcp/entities/1/outlets/2/removed-drafts", "", nil, key.Token, 404)
	req("GET", "/mcp/entities/2/outlets/4/removed-drafts", "", nil, key.Token, 404)
	seed(`UPDATE memberships SET role='read_only' WHERE id=1;`)
	req("GET", mcpRemoved, "", nil, key.Token, 403)
	seed(`UPDATE memberships SET role='purchasing' WHERE id=1;`)
	req("POST", restore, fmt.Sprintf(`{"motivo":" ","expected_revision":%d}`, revision), manager, "", 422)
	req("POST", restore, fmt.Sprintf(`{"motivo":"%s","expected_revision":%d}`, strings.Repeat("a", 2001), revision), manager, "", 422)
	req("POST", restore, fmt.Sprintf(`{"motivo":"Stale","expected_revision":%d}`, revision-1), manager, "", 409)
	seed(`CREATE FUNCTION reject_synthetic_restore() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF NEW.action='restore' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END; $$;
	CREATE TRIGGER reject_synthetic_restore BEFORE INSERT ON document_audit FOR EACH ROW EXECUTE FUNCTION reject_synthetic_restore();`)
	req("POST", restore, body, manager, "", 500)
	req("GET", doc, "", manager, "", 404)
	seed(`DROP TRIGGER reject_synthetic_restore ON document_audit;`)
	var restored struct{ ID, Revision int64 }
	decode(req("POST", restore, body, manager, "", 200), &restored)
	if restored.ID != created.ID || restored.Revision != revision+1 {
		t.Fatalf("restore changed identity or returned wrong revision: %+v", restored)
	}
	req("POST", restore, body, manager, "", 404)
	req("GET", removed+target, "", manager, "", 404)
	req("GET", mcpRemoved, "", nil, key.Token, 200)
	req("POST", docs, draft, manager, "", 409)
	req("GET", doc, "", reader, "", 200)
	req("GET", attachment, "", reader, "", 403)
	if got := req("GET", attachment, "", manager, "", 200); !bytes.Equal(got, png) {
		t.Fatal("original image changed")
	}
	if detail := req("GET", mcpDoc, "", nil, key.Token, 200); bytes.Contains(detail, []byte("private-sentinel.png")) {
		t.Fatalf("MCP disclosed private image filename: %s", detail)
	}
	var history string
	if err := db.QueryRowContext(ctx, `SELECT string_agg(action||':'||actor_id||':'||reason, ',' ORDER BY id) FROM document_audit WHERE document_id=$1`, created.ID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(history, "create:1:") || !strings.HasSuffix(history, ",delete:1:Baja original,restore:1:Corrección operativa") {
		t.Fatalf("original audit history missing: %s", history)
	}
	seed(`INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,
		currency,total_minor,description,status,revision,created_at,updated_at,deleted_at)
		SELECT i,1,CASE WHEN i=133 THEN 2 ELSE 1 END,1,'30711111111','invoice','A',3,i,'2026-11-27',
			'ARS',100,'',CASE WHEN i=132 THEN 'reviewed' ELSE 'draft' END,2,'before','after','removed'
		FROM generate_series(100,133) i;`)
	var first, second removedDraftPage
	decode(req("GET", removed, "", manager, "", 200), &first)
	if len(first.Items) != 30 || first.Items[0].ID != 131 || first.Before == nil || *first.Before != 102 {
		t.Fatalf("wrong first page: %+v", first)
	}
	decode(req("GET", mcpRemoved+"?before_id=102", "", nil, key.Token, 200), &second)
	if len(second.Items) != 2 || second.Items[0].ID != 101 || second.Items[1].ID != 100 || second.Before != nil {
		t.Fatalf("wrong last page: %+v", second)
	}
	req("POST", removed+"/132/restore", `{"motivo":"Reviewed exclusion","expected_revision":2}`, manager, "", 404)

}
