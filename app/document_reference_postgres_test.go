package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresPrintedReferenceFindersScopeAndPagination(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic-reference-fixture"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
		VALUES(1,'reference-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
	INSERT INTO entities(id,name,cuit,active) VALUES(1,'First','00000000001',TRUE),(2,'Second','00000000002',TRUE);
	INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Target',TRUE),(2,1,'Also granted',TRUE),
		(3,1,'Ungranted',TRUE),(4,2,'Foreign',TRUE),(5,1,'Inactive',FALSE);
	INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'read_only',TRUE);
	INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,5);
	INSERT INTO suppliers(id,entity_id,name,cuit,active)
	SELECT i,1,'private name sentinel '||i,LPAD(i::text,11,'0'),i <> 69 FROM generate_series(1,72) i;
	INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(73,2,'foreign sentinel','00000000073',TRUE);
	INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,
		currency,total_minor,description,status,revision,created_at,updated_at,deleted_at)
	SELECT i,1,1,i,LPAD(i::text,11,'0'),'credit_note','',12,CASE WHEN i<=35 THEN 346 ELSE 345 END,'2026-11-01',
		'ARS',25,'private note sentinel',CASE WHEN i%2=0 THEN 'reviewed' ELSE 'draft' END,1,'time','time',CASE WHEN i=68 THEN 'deleted' END
	FROM generate_series(1,68) i;
	INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,
		currency,total_minor,description,status,revision,created_at,updated_at)
	VALUES (69,1,1,69,'00000000069','debit_note','',12,345,'2026-11-01','ARS',25,'private sentinel','draft',1,'t','t'),
		(70,1,1,70,'00000000070','credit_note','A',12,345,'2026-11-01','ARS',25,'private sentinel','draft',1,'t','t'),
		(71,1,2,71,'00000000071','credit_note','',12,345,'2026-11-01','USD',25,'private sentinel','draft',1,'t','t'),
		(72,1,3,72,'00000000072','credit_note','',12,345,'2026-11-01','ARS',25,'private sentinel','draft',1,'t','t'),
		(73,2,4,73,'00000000073','credit_note','',12,345,'2026-11-01','ARS',25,'private sentinel','draft',1,'t','t');`); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com", enableMCP: true}
	router := a.routes()
	cookies := postgresTestCookies(t, router, "reference-fixture", pass)
	req := func(method, path, body, token string, browser bool, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "http://example.com/api/v1/"+path, strings.NewReader(body)).WithContext(ctx)
		if browser {
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d, want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	issue := func(body string) string {
		t.Helper()
		var out struct{ Token string }
		if err := json.Unmarshal(req("POST", "auth/mcp-token", body, "", true, 201), &out); err != nil || len(out.Token) != 43 {
			t.Fatal("could not issue synthetic token", err)
		}
		return out.Token
	}
	// Issue as purchasing, then prove current read_only is enough to use
	// these ordinary list reads without gaining review authority.
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='purchasing'"); err != nil {
		t.Fatal(err)
	}
	narrow := issue(`{"entity_id":1,"outlet_id":1}`)
	broad := issue(`{"entity_id":1}`)
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='read_only'"); err != nil {
		t.Fatal(err)
	}
	const query = "document_type=credit_note&letter=&point_of_sale=12&number=345"
	const mcp1 = "mcp/entities/1/outlets/1/documents"
	const native1 = "entities/1/documents"
	readMCP := func(path, token string, want int) []byte { return req("GET", path, "", token, false, want) }
	readNative := func(path string, want int) []byte { return req("GET", path, "", "", true, want) }
	ids := func(raw []byte, key string) (result []int64, next int64) {
		t.Helper()
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wire); err != nil || len(wire["printed_reference"]) == 0 {
			t.Fatalf("missing acknowledged reference: %s", raw)
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(wire[key], &rows); err != nil || rows == nil {
			t.Fatalf("bad matching rows: %s", raw)
		}
		for _, row := range rows {
			var id int64
			if err := json.Unmarshal(row["id"], &id); err != nil || id == 0 || key == "matches" && len(row) != 3 {
				t.Fatalf("bad match projection: %v", row)
			}
			result = append(result, id)
		}
		if value := wire["next_id"]; len(value) > 0 {
			if err := json.Unmarshal(value, &next); err != nil {
				t.Fatal(err)
			}
		}
		for _, forbidden := range []string{"sentinel", "issuer", "cuit", "description", "attachment"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("unexpected protected data in %s", raw)
			}
		}
		return
	}
	for _, support := range []struct {
		key, path string
		read      func(string) []byte
	}{
		{"documents", mcp1 + "?" + query, func(path string) []byte { return readMCP(path, narrow, 200) }},
		{"matches", native1 + "?outlet_id=1&" + query, func(path string) []byte { return readNative(path, 200) }},
	} {
		first, next := ids(support.read(support.path), support.key)
		want := make([]int64, 30)
		for i := range want {
			want[i] = int64(36 + i)
		}
		if !reflect.DeepEqual(first, want) || next != 65 {
			t.Fatalf("%s first filtered page = %v cursor %d", support.key, first, next)
		}
		second, next := ids(support.read(fmt.Sprintf("%s&after_id=%d", support.path, next)), support.key)
		if !reflect.DeepEqual(second, []int64{66, 67}) || next != 0 {
			t.Fatalf("%s second filtered page = %v cursor %d", support.key, second, next)
		}
	}
	if old := readMCP(mcp1+"?after_id=1", narrow, 200); strings.Contains(string(old), "printed_reference") || !strings.Contains(string(old), `"id":2`) {
		t.Fatalf("old list changed: %s", old)
	}
	if old := readNative(native1+"?outlet_id=1", 200); !strings.HasPrefix(string(old), "[") {
		t.Fatal("old browser response shape changed")
	}
	for _, invalid := range []string{"number=345", strings.Replace(query, "letter=", "letter=a", 1), query + "&issuer_cuit=x", query + "&letter=A"} {
		readMCP(mcp1+"?"+invalid, narrow, 422)
		readNative(native1+"?outlet_id=1&"+invalid, 422)
	}
	readNative(native1+"?"+query, 422)
	readMCP("mcp/entities/1/outlets/2/documents?"+query, narrow, 404)
	for _, route := range []string{"mcp/entities/1/outlets/3/documents", "mcp/entities/1/outlets/5/documents", "mcp/entities/2/outlets/4/documents"} {
		readMCP(route+"?"+query, broad, 404)
	}
	readNative(native1+"?outlet_id=3&"+query, 404)
	readNative(native1+"?outlet_id=5&"+query, 404)
	readNative("entities/2/documents?outlet_id=4&"+query, 404)
	other, next := ids(readNative(native1+"?outlet_id=2&"+query, 200), "matches")
	if !reflect.DeepEqual(other, []int64{71}) || next != 0 {
		t.Fatal("other granted outlet incorrectly included target results", other)
	}
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='unassigned'"); err != nil {
		t.Fatal(err)
	}
	readMCP(mcp1+"?"+query, narrow, 403)
	readNative(native1+"?outlet_id=1&"+query, 403)
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='purchasing'; DELETE FROM outlet_grants WHERE outlet_id=1"); err != nil {
		t.Fatal(err)
	}
	readMCP(mcp1+"?"+query, narrow, 404)
	readNative(native1+"?outlet_id=1&"+query, 404)
}
