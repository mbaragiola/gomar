package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestPostgresPOSStockConcurrentSale(t *testing.T) {
	ctx, db := testPostgresDB(t)
	var existing bool
	if err := db.QueryRowContext(ctx, `SELECT to_regnamespace('gomar') IS NOT NULL`).Scan(&existing); err != nil || existing {
		t.Fatalf("requires empty QA database: schema present=%t, error=%v", existing, err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	const password = "synthetic-pos-stock-password"
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
         VALUES(1,'fixture-admin',$1,TRUE,FALSE,FALSE),(2,'fixture-cashier',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	commands := []string{
		`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture Entity','00000000001',TRUE)`,
		`INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Fixture A',TRUE),(2,1,'Fixture B',TRUE)`,
		`INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'purchasing',TRUE)`,
		`INSERT INTO outlet_grants(membership_id,outlet_id) VALUES(1,1),(2,1)`,
	}
	for _, query := range commands {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	app := app{db: db, host: "example.com", enablePOS: true, enableStock: true, enableDelivery: true}
	server := app.routes()
	ask := func(method, path, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		result := httptest.NewRecorder()
		server.ServeHTTP(result, r)
		return result
	}
	expect := func(r *httptest.ResponseRecorder, code int) *httptest.ResponseRecorder {
		t.Helper()
		if r.Code != code || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d want=%d cache=%q body=%s", r.Code, code, r.Header().Get("Cache-Control"), r.Body)
		}
		return r
	}
	login := func(name string) []*http.Cookie {
		t.Helper()
		csrf := expect(ask("GET", "/api/v1/auth/csrf", "", nil), 200).Result().Cookies()
		logged := expect(ask("POST", "/api/v1/auth/login", `{"usuario":"`+name+`","clave":"`+password+`"}`, csrf), 200)
		var effective []*http.Cookie
		for _, group := range [][]*http.Cookie{logged.Result().Cookies(), csrf} {
			for _, c := range group {
				found := false
				for _, stored := range effective {
					if stored.Name == c.Name {
						found = true
					}
				}
				if !found {
					effective = append(effective, c)
				}
			}
		}
		return effective
	}
	admin, cashier := login("fixture-admin"), login("fixture-cashier")
	const base = "/api/v1/entities/1/outlets/1/pos"
	const stock = base + "/stock"
	const moves = stock + "/products/1/movements"
	expect(ask("GET", stock, "", nil), 401)
	expect(ask("GET", "/api/v1/entities/1/outlets/2/pos/stock", "", cashier), 404)
	expect(ask("PUT", stock+"/settings", `{"activo":true}`, cashier), 403)
	expect(ask("POST", base+"/catalog", `{"nombre":"Tostado sintético","precio_centavos":1050,"activo":true}`, admin), 201)
	expect(ask("PUT", stock+"/settings", `{"activo":true}`, admin), 200)
	expect(ask("PUT", stock+"/products/1", `{"activo":true,"minimo":2}`, admin), 200)
	expect(ask("POST", moves, `{"id_solicitud":"abcdefff-1200-4000-8000-000000000001","tipo":"recepcion","cantidad":5}`, cashier), 201)

	// Hold the production outlet lock so two genuinely pooled requests queue
	// before either can evaluate the idempotency read.
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if err := lockPostgresOutlet(ctx, blocker, 1); err != nil {
		t.Fatal(err)
	}
	const sale = `{"id_solicitud":"abcdefff-1200-4000-8000-000000000002","actor_id":2,"metodo_pago":"debito","items":[{"producto_id":1,"revision":1,"cantidad":2}]}`
	answers := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { answers <- ask("POST", base+"/sales", sale, cashier) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().InUse < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if db.Stats().InUse < 3 {
		t.Fatal("did not observe two pooled request transactions behind the outlet lock")
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	var replies []*httptest.ResponseRecorder
	for range 2 {
		select {
		case r := <-answers:
			replies = append(replies, r)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	sort.Slice(replies, func(i, j int) bool { return replies[i].Code < replies[j].Code })
	var first, second posSale
	for i, r := range replies {
		var target *posSale
		if i == 0 {
			expect(r, 200)
			target = &first
		} else {
			expect(r, 201)
			target = &second
		}
		if err := json.Unmarshal(r.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(first, second) || first.Total != 2100 || first.PaymentMethod != "debito" {
		t.Fatalf("mismatched or invalid receipts: %+v %+v", first, second)
	}
	expect(ask("POST", base+"/sales", strings.Replace(sale, `"cantidad":2`, `"cantidad":3`, 1), cashier), 409)
	var received struct {
		Movements []stockMovement `json:"movimientos"`
	}
	if err := json.Unmarshal(expect(ask("GET", moves, "", cashier), 200).Body.Bytes(), &received); err != nil {
		t.Fatal(err)
	}
	if len(received.Movements) != 2 || received.Movements[0].Kind != "venta" || received.Movements[0].Delta != -2 || received.Movements[1].Kind != "recepcion" {
		t.Fatalf("expected one sale deduction: %+v", received)
	}
	const count = `{"id_solicitud":"abcdefff-1200-4000-8000-000000000003","tipo":"conteo","cantidad":1}`
	original := expect(ask("POST", moves, count, cashier), 201).Body.String()
	if replay := expect(ask("POST", moves, count, cashier), 200).Body.String(); replay != original {
		t.Fatal("count retry changed the receipt")
	}
	var summary struct {
		Enabled  bool           `json:"activo"`
		Products []stockProduct `json:"productos"`
	}
	if err := json.Unmarshal(expect(ask("GET", stock, "", cashier), 200).Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.Enabled || len(summary.Products) != 1 || summary.Products[0].Balance != 1 || summary.Products[0].Warning != "bajo" {
		t.Fatalf("stock summary: %+v", summary)
	}
	const wiki = "/api/v1/entities/1/outlets/1/knowledge"
	expect(ask("POST", wiki, `{"titulo":"Café 100%","contenido":"Nota de prueba"}`, admin), 201)
	if w := expect(ask("GET", wiki+"?q=100%25", "", cashier), 200); !strings.Contains(w.Body.String(), "Nota de prueba") {
		t.Fatalf("literal wiki search: %s", w.Body)
	}
	if w := expect(ask("GET", wiki+"?q=%25_", "", cashier), 200); strings.Contains(w.Body.String(), "Nota de prueba") {
		t.Fatalf("wildcard characters must be literal: %s", w.Body)
	}
	expect(ask("GET", "/api/v1/entities/1/outlets/2/knowledge", "", cashier), 404)
	expect(ask("GET", "/api/v1/entities/1/documents", "", admin), 200)
	if _, err := db.ExecContext(ctx, `INSERT INTO delivery_sources VALUES ('rappi','rappi-synthetic',1),('uber_eats','uber-synthetic',2)`); err != nil {
		t.Fatal(err)
	}
	for _, event := range []deliveryEvent{
		{Provider: "rappi", StoreID: "rappi-synthetic", OrderID: "same-external-id", EventID: "qa-1", Kind: deliveryReceived, Items: []deliveryItem{{Name: "Rappi de prueba", Quantity: 1}}},
		{Provider: "uber_eats", StoreID: "uber-synthetic", OrderID: "same-external-id", EventID: "qa-1", Kind: deliveryAccepted, Items: []deliveryItem{{Name: "Uber de prueba", Quantity: 1}}},
	} {
		first, err := ingestDelivery(ctx, db, event, time.Now())
		if err != nil || first.Seen {
			t.Fatalf("seed sample delivery: %+v %v", first, err)
		}
		repeat, err := ingestDelivery(ctx, db, event, time.Now())
		if err != nil || !repeat.Seen || repeat.OrderID != first.OrderID {
			t.Fatalf("repeat sample delivery: %+v %v", repeat, err)
		}
	}
	const inbox = "/delivery/orders"
	if w := expect(ask("GET", "/api/v1/entities/1/outlets/1"+inbox, "", cashier), 200); !strings.Contains(w.Body.String(), `"estado":"recibido"`) || strings.Contains(w.Body.String(), "Uber de prueba") {
		t.Fatalf("unexpected delivery outlet or status: %s", w.Body)
	}
	expect(ask("GET", "/api/v1/entities/1/outlets/2"+inbox, "", cashier), 404)
}
