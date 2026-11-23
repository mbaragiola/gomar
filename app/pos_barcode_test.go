package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func TestPOSBarcodeExactText(t *testing.T) {
	for _, code := range []string{"", "000719", "Ab-42.!~", strings.Repeat("a", 80)} {
		if !validPOSBarcode(code) {
			t.Fatalf("valid exact code refused: %q", code)
		}
	}
	for _, code := range []string{" café", "01\n", "\t01", "AB 01", "ß", strings.Repeat("a", 81)} {
		if validPOSBarcode(code) {
			t.Fatalf("invalid code accepted: %q", code)
		}
	}
	for _, body := range []string{
		`{"product_id":1,"expected_revision":2,"barcode":12}`,
		`{"product_id":1,"expected_revision":2,"barcode":null}`,
		`{"product_id":1,"expected_revision":2}`,
		`{"product_id":1,"expected_revision":2,"barcode":"12","barcode":"13"}`,
	} {
		if _, err := decodePOSBarcode([]byte(body)); err == nil {
			t.Fatalf("ambiguous code accepted: %s", body)
		}
	}
	const raw = `{"product_id":1,"expected_revision":2,"barcode":"000719"}`
	in, err := decodePOSBarcode([]byte(raw))
	if err != nil || in.Barcode != "000719" {
		t.Fatal(in, err)
	}
}

func TestPostgresPOSBarcode(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "fixture-only barcode password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
	 (1,'fixture-owner',$1,true,false,false),(2,'fixture-clerk',$1,true,false,false)`, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
	 INSERT INTO entities(id,name,cuit,active) VALUES(1,'A','00000000000',true),(2,'B','00000000001',true);
	 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Uno',true),(2,1,'Dos',true),(3,2,'Tres',true);
	 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',true),(2,2,1,'purchasing',true);
	 INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1);
	 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1),(2,1,1),(3,2,1),(4,1,1),(5,3,1);
	 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES
	 (1,1,1,'First',200,true),(2,1,1,'Second',300,false),(3,2,1,'Other outlet',400,true),
	 (4,1,1,'Fourth',600,true),(5,3,1,'Private other business',700,true);`); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com", enablePOS: true, enableMCP: true, enableMCPPOSCatalogProposals: true}
	h := a.routes()
	owner := postgresTestCookies(t, h, "fixture-owner", pass)
	otherLogin := postgresTestCookies(t, h, "fixture-owner", pass)
	clerk := postgresTestCookies(t, h, "fixture-clerk", pass)
	ask := func(handler http.Handler, method, path, body, bearer string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if cookies != nil {
			r.Header.Set("Origin", "http://example.com")
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	must := func(w *httptest.ResponseRecorder, want int) *httptest.ResponseRecorder {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status=%d want=%d: %s", w.Code, want, w.Body)
		}
		return w
	}
	call := func(method, path, body, bearer string, cookies []*http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		return must(ask(h, method, path, body, bearer, cookies), want)
	}
	token := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var v struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || len(v.Token) != 43 {
			t.Fatal("missing synthetic credential")
		}
		return v.Token
	}
	const browser = "/api/v1/entities/1/outlets/1/"
	const mcp = "/api/v1/mcp/entities/1/outlets/1/"
	const change = `{"revision":1,"codigo_barras":"000719"}`
	const target = browser + "pos/catalog/1/barcode"
	call("PUT", target, change, "", clerk, 403)
	call("PUT", "/api/v1/entities/2/outlets/3/pos/catalog/5/barcode", change, "", owner, 404)
	call("PUT", "/api/v1/entities/1/outlets/2/pos/catalog/1/barcode", change, "", owner, 404)
	call("PUT", target, `{"revision":1,"codigo_barras":719}`, "", owner, 422)
	call("PUT", target, `{"revision":1}`, "", owner, 422)
	if w := call("PUT", target, change, "", owner, 200); !strings.Contains(w.Body.String(), `"codigo_barras":"000719"`) {
		t.Fatal("native change lost exact text")
	}
	call("PUT", target, change, "", owner, 409)
	call("PUT", browser+"pos/catalog/2/barcode", change, "", owner, 409)
	call("PUT", "/api/v1/entities/1/outlets/2/pos/catalog/3/barcode", change, "", owner, 200)
	call("PUT", browser+"pos/catalog/2/barcode", `{"revision":1,"codigo_barras":"000719I"}`, "", owner, 200)
	call("PUT", browser+"pos/catalog/4/barcode", `{"revision":1,"codigo_barras":"000719I"}`, "", owner, 409)

	// An older catalog editor changes the price using its existing body shape
	// without dropping the barcode or altering earlier product versions.
	call("PUT", browser+"pos/catalog/1", `{"revision":2,"nombre":"First","precio_centavos":225,"activo":true}`, "", owner, 200)
	catalog := call("GET", browser+"pos/catalog", "", "", clerk, 200)
	if !strings.Contains(catalog.Body.String(), `"codigo_barras":"000719"`) {
		t.Fatal("legacy update dropped barcode")
	}
	read := token(call("POST", "/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, "", clerk, 201))
	matches := call("GET", mcp+"pos-catalog?barcode=000719", "", read, nil, 200)
	if v := matches.Body.String(); !strings.Contains(v, `"barcode":"000719"`) || strings.Contains(v, "Other outlet") {
		t.Fatal("lookup returned wrong catalog payload")
	}
	if w := call("GET", mcp+"pos-catalog?barcode=719", "", read, nil, 200); !strings.Contains(w.Body.String(), `"products":[]`) {
		t.Fatal("lookup normalized away leading zeros")
	}
	call("GET", mcp+"pos-catalog?barcode=000719&barcode=000719", "", read, nil, 422)
	call("GET", mcp+"pos-catalog?barcode=000719&after_product_id=1", "", read, nil, 422)
	call("GET", "/api/v1/mcp/entities/1/outlets/2/pos-catalog?barcode=000719", "", read, nil, 404)
	call("GET", "/api/v1/mcp/entities/2/outlets/3/pos-catalog?barcode=000719", "", read, nil, 404)
	if w := call("GET", mcp+"pos-catalog/1", "", read, nil, 200); !strings.Contains(w.Body.String(), `"barcode":"000719"`) {
		t.Fatal("exact product read did not return its code")
	}

	const session = `{"entity_id":1,"outlet_id":1,"actions":["pos_product_barcode_change"]}`
	call("POST", proposalSessionsAuthPath, session, "", clerk, 403)
	edit := token(call("POST", proposalSessionsAuthPath, session, "", owner, 201))
	const proposal = `{"request_id":"55555555-0000-4000-8000-000000000001","change":{"product_id":1,"expected_revision":3,"barcode":""}}`
	call("POST", mcp+"pos-barcode-proposals", proposal, read, nil, 401)
	call("POST", "/api/v1/mcp/entities/1/outlets/2/pos-barcode-proposals", proposal, edit, nil, 404)
	receipt := call("POST", mcp+"pos-barcode-proposals", proposal, edit, nil, 201)
	if strings.Contains(receipt.Body.String(), "barcode") {
		t.Fatal("proposal receipt exposed its private preview")
	}
	call("GET", mcp+"pos-catalog?barcode=000719", "", read, nil, 200)
	var saved posCatalogWireReceipt
	if err := json.Unmarshal(receipt.Body.Bytes(), &saved); err != nil || saved.ID < 1 {
		t.Fatal("missing proposal receipt")
	}
	path := fmt.Sprintf("%spos-barcode-proposals/%d", browser, saved.ID)
	call("GET", path, "", "", otherLogin, 404)
	var detail posCatalogWireDetails
	if err := json.Unmarshal(call("GET", path, "", "", owner, 200).Body.Bytes(), &detail); err != nil ||
		detail.Change == nil || detail.Change.Barcode == nil || *detail.Change.Barcode != "" ||
		detail.Change.CurrentBarcode == nil || *detail.Change.CurrentBarcode != "000719" {
		t.Fatalf("incorrect browser proposal preview: %+v", detail.Change)
	}
	decision := fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, detail.Digest)
	call("POST", path+"/decision", strings.Replace(decision, "true", "false", 1), "", owner, 422)
	call("POST", path+"/decision", decision, "", otherLogin, 404)
	call("POST", path+"/decision", decision, "", owner, 200)
	if w := call("GET", mcp+"pos-catalog?barcode=000719", "", read, nil, 200); !strings.Contains(w.Body.String(), `"products":[]`) {
		t.Fatal("approved clear did not remove the mapping")
	}

	// Two regular requests may race on different products for the same code.
	// Only one can commit; the existing outlet lock and unique index agree.
	ch := make(chan int, 2)
	for _, id := range []int{1, 4} {
		go func() {
			rev := 1
			if id == 1 {
				rev = 4
			}
			payload := fmt.Sprintf(`{"revision":%d,"codigo_barras":"RACE-001"}`, rev)
			w := ask(h, "PUT", fmt.Sprintf("%spos/catalog/%d/barcode", browser, id), payload, "", owner)
			ch <- w.Code
		}()
	}
	results := []int{<-ch, <-ch}
	sort.Ints(results)
	if results[0] != 200 || results[1] != 409 {
		t.Fatal("duplicate race committed the wrong number of changes", results)
	}
	var sales, mapped int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM pos_sales), (SELECT count(*) FROM pos_products WHERE barcode='RACE-001')`).Scan(&sales, &mapped); err != nil || sales != 0 || mapped != 1 {
		t.Fatal(sales, mapped, err)
	}
	off := (&app{db: db, host: "example.com", enableMCP: true}).routes()
	must(ask(off, "PUT", target, change, "", owner), 404)
	must(ask(off, "GET", mcp+"pos-catalog?barcode=RACE-001", "", read, nil), 404)
}
