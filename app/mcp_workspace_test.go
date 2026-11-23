package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func TestPostgresMCPWorkspaceReadsKeepOutletAndRoleScope(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-workspace-pass"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
   VALUES(1,'workspace-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	fixture := `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic entity','00000000001',TRUE),(2,'Other synthetic entity','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES
 (1,1,'First outlet',TRUE),(2,1,'Second outlet',TRUE),(3,1,'Ungranted outlet',TRUE),(4,2,'Foreign tenant',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES (1,1,'supplier-must-not-leak','00000000003',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,
      point_of_sale,number,issue_date,due_date,currency,total_minor,description,status,revision,created_at,updated_at)
 VALUES (1,1,1,1,'00000000003','invoice','A',1,1,'2026-11-01','2026-11-10','ARS',10250,'','reviewed',1,
      '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z'),
        (2,1,1,1,'00000000003','invoice','A',1,2,'2026-11-01','2026-11-20','USD',275,'','reviewed',1,
      '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z'),
        (3,1,2,1,'00000000003','invoice','A',1,3,'2026-11-01','2026-11-09','ARS',99999,'','reviewed',1,
      '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z');
 INSERT INTO delivery_sources(provider,external_store_id,outlet_id)
 VALUES('rappi','secret-external-store',1),('pedidosya','other-secret-store',2);
 `
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	a := &app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableStock: true, enableDelivery: true,
		mcpClock: func() time.Time { return now }, payablesClock: func() time.Time { return now }}
	mux := a.routes()
	cookies := postgresTestCookies(t, mux, "workspace-fixture", password)
	invoke := func(handler http.Handler, method, path, body, token string, auth bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if auth {
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d, want %d", method, path, w.Code, want)
		}
		if token != "" && want != 404 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("MCP response must not be stored")
		}
		return w
	}
	mutate := func(method, path, body string, want int) string {
		t.Helper()
		return invoke(mux, method, "/api/v1/entities/1"+path, body, "", true, want).Body.String()
	}
	mutate("POST", "/outlets/1/knowledge", `{"titulo":"Turno mañana","contenido":"Receta privada de prueba"}`, 201)
	mutate("POST", "/outlets/2/knowledge", `{"titulo":"No disponible","contenido":"Contenido de otra sucursal"}`, 201)
	mutate("POST", "/outlets/1/pos/catalog", `{"nombre":"Sándwich sintético","precio_centavos":100,"activo":true}`, 201)
	mutate("PUT", "/outlets/1/pos/stock/settings", `{"activo":true}`, 200)
	mutate("PUT", "/outlets/1/pos/stock/products/1", `{"activo":true,"minimo":8}`, 200)
	mutate("POST", "/outlets/1/pos/stock/products/1/movements", `{"id_solicitud":"11111111-0000-4000-8000-000000000001","tipo":"recepcion","cantidad":7}`, 201)
	for id := 1; id <= 3; id++ {
		mutate("POST", fmt.Sprintf("/documents/%d/payable", id), `{"motivo":"Alta de prueba"}`, 201)
	}
	for _, event := range []deliveryEvent{
		{"rappi", "secret-external-store", "secret-order-one", "secret-event-one", deliveryReceived, []deliveryItem{{"private item one", 1}}},
		{"rappi", "secret-external-store", "secret-order-two", "secret-event-two", deliveryCancelled, nil},
		{"pedidosya", "other-secret-store", "secret-order-three", "secret-event-three", deliveryAccepted, []deliveryItem{{"ungranted item", 1}}},
	} {
		if _, err := ingestDelivery(ctx, db, event, now); err != nil {
			t.Fatal(err)
		}
	}
	issue := func(body string) string {
		t.Helper()
		var v struct{ Token string }
		r := invoke(mux, "POST", "/api/v1/auth/mcp-token", body, "", true, 201)
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil || len(v.Token) != 43 {
			t.Fatal("failed to issue synthetic token")
		}
		return v.Token
	}
	token := issue(`{"entity_id":1,"outlet_id":1}`)
	broader := issue(`{"entity_id":1}`)
	const base = "/api/v1/mcp/entities/1/outlets/1"
	read := func(path string, status int) string {
		t.Helper()
		return invoke(mux, "GET", base+path, "", token, false, status).Body.String()
	}
	decode := func(data string, dst any) {
		t.Helper()
		if err := json.Unmarshal([]byte(data), dst); err != nil {
			t.Fatal(err)
		}
	}
	search := read("/knowledge?q=privada", 200)
	var knowledge mcpclient.KnowledgeList
	decode(search, &knowledge)
	if len(knowledge.Articles) != 1 || knowledge.Articles[0].ID != 1 || strings.Contains(search, "Receta privada") {
		t.Fatal("search leaked content or lost match")
	}
	var article mcpclient.Article
	decode(read("/knowledge/1", 200), &article)
	if article.Body != "Receta privada de prueba" {
		t.Fatal("wrong article")
	}
	read("/knowledge/2", 404)
	read("/knowledge?q=1&q=2", 422)
	var stock mcpclient.Stock
	decode(read("/stock", 200), &stock)
	if !stock.Enabled || len(stock.Products) != 1 || stock.Products[0].Balance != 7 || stock.Products[0].Alert != "low" {
		t.Fatal(stock)
	}
	decode(read("/stock?after_product_id=1", 200), &stock)
	if len(stock.Products) != 0 {
		t.Fatal("cursor included previous page")
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO pos_products(id,outlet_id,current_revision) SELECT i,1,1 FROM generate_series(2,33) i;
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
 SELECT i,1,1,'private stock sentinel '||i,100,true FROM generate_series(2,33) i;
 INSERT INTO stock_products(outlet_id,product_id,enabled,minimum)
 SELECT 1,i,i<>33,0 FROM generate_series(2,33) i;
 INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
 VALUES(1,2,1,'merma',1,-1,'2026-11-01T00:00:00Z','11111111-0000-4000-8000-000000000002'),
       (1,4,1,'recepcion',1,1,'2026-11-01T00:00:00Z','11111111-0000-4000-8000-000000000004')`); err != nil {
		t.Fatal(err)
	}
	rawStocks := read("/stock-alert-summary", 200)
	var alert mcpclient.StockAlertSummary
	decode(rawStocks, &alert)
	if alert != (mcpclient.StockAlertSummary{Enabled: true, Tracked: 32, Negative: 1, AtOrBelowMinimum: 30, Healthy: 1}) || strings.Contains(rawStocks, "sentinel") {
		t.Fatal(alert)
	}
	var nativeStock struct {
		Enabled  bool           `json:"activo"`
		Products []stockProduct `json:"productos"`
	}
	decode(invoke(mux, "GET", "/api/v1/entities/1/outlets/1/pos/stock", "", "", true, 200).Body.String(), &nativeStock)
	for _, id := range []int64{1, 2, 4, 33} {
		var item mcpclient.StockProductDetails
		raw := read(fmt.Sprintf("/stock/%d", id), 200)
		decode(raw, &item)
		p := nativeStock.Products[id-1]
		warning := map[string]string{"negativo": "negative", "bajo": "low"}[p.Warning]
		if item != (mcpclient.StockProductDetails{ID: p.ID, OutletEnabled: nativeStock.Enabled,
			ProductEnabled: p.Enabled, Minimum: p.Minimum, Balance: p.Balance, Alert: warning}) ||
			strings.Contains(raw, "name") || strings.Contains(raw, "sentinel") {
			t.Fatal("exact read disagrees with native stock or exposes non-stock fields", raw)
		}
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(100,1,1),(101,2,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
 VALUES(100,1,1,'private unconfigured',100,true),(101,2,1,'private other branch',100,true);
 INSERT INTO stock_outlets(outlet_id,enabled) VALUES(2,FALSE);
 INSERT INTO stock_products(outlet_id,product_id,enabled,minimum) VALUES(2,101,true,12);`); err != nil {
		t.Fatal(err)
	}
	if missing := read("/stock/999", 404); missing != read("/stock/100", 404) || missing != read("/stock/101", 404) || missing != read("/stock/0", 404) {
		t.Fatal("a foreign or unconfigured product is distinguishable from a missing one")
	}
	read("/stock/1?extra=1", 422)
	read("/stock/1?", 422)
	var outside mcpclient.StockProductDetails
	decode(invoke(mux, "GET", "/api/v1/mcp/entities/1/outlets/2/stock/101", "", broader, false, 200).Body.String(), &outside)
	if outside != (mcpclient.StockProductDetails{ID: 101, OutletEnabled: false, ProductEnabled: true, Minimum: 12}) {
		t.Fatal("disabled outlet must suppress alert but preserve product state", outside)
	}
	for _, role := range []string{"purchasing", "accounting", "read_only"} {
		if _, err := db.ExecContext(ctx, `UPDATE memberships SET role=$1 WHERE id=1`, role); err != nil {
			t.Fatal(err)
		}
		status := 200
		if role == "read_only" {
			status = 403
		}
		read("/stock/1", status)
		invoke(mux, "GET", "/api/v1/entities/1/outlets/1/pos/stock", "", "", true, status)
	}
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='administration' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	compare := mcpclient.StockAlertSummary{Enabled: nativeStock.Enabled}
	for _, product := range nativeStock.Products {
		if !product.Enabled {
			continue
		}
		compare.Tracked++
		switch product.Warning {
		case "negativo":
			compare.Negative++
		case "bajo":
			compare.AtOrBelowMinimum++
		case "":
			compare.Healthy++
		}
	}
	if alert != compare || len(nativeStock.Products) != 33 {
		t.Fatal(compare)
	}
	decode(invoke(mux, "GET", "/api/v1/mcp/entities/1/outlets/2/stock-alert-summary", "", broader, false, 200).Body.String(), &alert)
	if alert != (mcpclient.StockAlertSummary{}) {
		t.Fatal(alert)
	}
	read("/stock-alert-summary?limit=30", 422)

	delivery := read("/delivery-orders", 200)
	var orders mcpclient.Delivery
	decode(delivery, &orders)
	if len(orders.Orders) != 2 || orders.Orders[0].Status != "cancelled" || orders.Orders[1].Status != "received" ||
		strings.Contains(delivery, "secret") || strings.Contains(delivery, "private item") {
		t.Fatal("unexpected delivery result")
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO delivery_sources(provider,external_store_id,outlet_id) VALUES('pedidosya','synthetic-window',1);
 INSERT INTO delivery_orders(provider,external_store_id,external_order_id,outlet_id,accepted_items,cancelled,received_at,updated_at)
 SELECT 'pedidosya','synthetic-window','order-'||i,1,'[]',false,'2026-10-01T00:00:00Z','2026-10-01T00:00:00Z'
 FROM generate_series(1,98) i;
 INSERT INTO delivery_orders(provider,external_store_id,external_order_id,outlet_id,accepted_items,cancelled,received_at,updated_at)
 VALUES('rappi','secret-external-store','outside-window',1,'[]',false,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var summary mcpclient.DeliveryStatusSummary
	rawSummary := read("/delivery-status-summary", 200)
	decode(rawSummary, &summary)
	if summary != (mcpclient.DeliveryStatusSummary{WindowLimit: 100, Received: 1, Accepted: 98, Cancelled: 1}) ||
		strings.Contains(rawSummary, "secret") || strings.Contains(rawSummary, "item") {
		t.Fatal(summary)
	}
	var native struct {
		Orders []struct {
			Status string `json:"estado"`
		} `json:"pedidos"`
	}
	decode(invoke(mux, "GET", "/api/v1/entities/1/outlets/1/delivery/orders", "", "", true, 200).Body.String(), &native)
	counts := map[string]int{}
	for _, item := range native.Orders {
		counts[item.Status]++
	}
	if len(native.Orders) != 100 || counts["recibido"] != summary.Received || counts["aceptado"] != summary.Accepted || counts["cancelado"] != summary.Cancelled {
		t.Fatal(counts)
	}
	read("/delivery-status-summary?limit=10", 422)
	read("/delivery-status-summary?date=2026-10-01", 422)

	report := read("/payables-summary", 200)
	var ap mcpclient.Payables
	decode(report, &ap)
	if ap.AsOf != "2026-11-20" || len(ap.Totals) != 2 || ap.Totals[0].OutstandingMinor != 10250 ||
		ap.Totals[0].OverdueMinor != 10250 || ap.Totals[1].OutstandingMinor != 275 || ap.Totals[1].OverdueMinor != 0 || strings.Contains(report, "supplier") {
		t.Fatal("unexpected scoped payables")
	}
	for _, suffix := range []string{"/knowledge", "/knowledge/1", "/stock", "/stock?alert=any", "/stock/1", "/stock-alert-summary", "/delivery-orders", "/delivery-status-summary", "/payables-summary"} {
		invoke(mux, "GET", "/api/v1/mcp/entities/1/outlets/2"+suffix, "", token, false, 404)
		invoke(mux, "GET", "/api/v1/mcp/entities/1/outlets/3"+suffix, "", broader, false, 404)
		invoke(mux, "GET", "/api/v1/mcp/entities/2/outlets/4"+suffix, "", token, false, 404)
	}
	// Sparse matches span multiple filtered pages; unfiltered rows and
	// disabled rows before and between matches must not consume the limit.
	if _, err := db.ExecContext(ctx, `
 INSERT INTO pos_products(id,outlet_id,current_revision) SELECT i,1,1 FROM generate_series(110,239) i;
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
 SELECT i,1,1,'Filtered fixture '||i,100,true FROM generate_series(110,239) i;
 INSERT INTO stock_products(outlet_id,product_id,enabled,minimum)
 SELECT 1,i,i%5<>0,0 FROM generate_series(110,239) i;
 INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
 SELECT 1,i,1,'recepcion',1,1,'2026-11-01T00:00:00Z','22222222-0000-4000-8000-'||lpad(i::text,12,'0')
 FROM generate_series(110,239) i WHERE i%2=0;`); err != nil {
		t.Fatal(err)
	}
	var first, second, switched, negative, disabledPage mcpclient.Stock
	raw := read("/stock", 200)
	if strings.Contains(raw, "alert_filter") {
		t.Fatal("unfiltered response changed")
	}
	decode(read("/stock?alert=low&after_product_id=110", 200), &first)
	if first.AlertFilter != "low" || !first.Enabled || len(first.Products) != 30 ||
		first.Products[0].ID != 111 || first.NextProductID != 183 || first.Products[29].ID != 183 {
		t.Fatal("wrong first sparse page", first)
	}
	decode(read(fmt.Sprintf("/stock?alert=low&after_product_id=%d", first.NextProductID), 200), &second)
	if second.AlertFilter != "low" || len(second.Products) != 22 || second.Products[0].ID != 187 ||
		second.Products[21].ID != 239 || second.NextProductID != 0 {
		t.Fatal("wrong last sparse page", second)
	}
	for _, p := range append(first.Products, second.Products...) {
		if p.ID%2 != 1 || p.ID%5 == 0 || p.Alert != "low" || p.Balance != 0 || p.Minimum != 0 {
			t.Fatal("filter admitted a non-match", p)
		}
	}
	decode(read("/stock?alert=negative", 200), &negative)
	if negative.AlertFilter != "negative" || len(negative.Products) != 1 || negative.Products[0].ID != 2 ||
		negative.Products[0].Alert != "negative" {
		t.Fatal("negative must win over low", negative)
	}
	decode(read("/stock?alert=negative&after_product_id=183", 200), &switched)
	if switched.AlertFilter != "negative" || len(switched.Products) != 0 || switched.NextProductID != 0 {
		t.Fatal("numeric cursor must remain an exclusive ID bound", switched)
	}
	var anyPage mcpclient.Stock
	decode(read("/stock?alert=any", 200), &anyPage)
	if anyPage.AlertFilter != "any" || len(anyPage.Products) != 30 || anyPage.Products[0].Alert != "low" ||
		anyPage.Products[1].Alert != "negative" {
		t.Fatal("any must include both alert classes", anyPage)
	}
	decode(invoke(mux, "GET", "/api/v1/mcp/entities/1/outlets/2/stock?alert=any", "", broader, false, 200).Body.String(), &disabledPage)
	if disabledPage.AlertFilter != "any" || disabledPage.Enabled || disabledPage.Products == nil || len(disabledPage.Products) != 0 || disabledPage.NextProductID != 0 {
		t.Fatal("disabled outlet must not expose filtered stock", disabledPage)
	}
	for _, query := range []string{"alert=", "alert=LOW", "alert=unknown", "alert=any&alert=any", "alert=low&after_product_id=", "alert=negative&after_product_id=0", "alert=any&after_product_id=1&after_product_id=2", "alert=low&invalid=1"} {
		read("/stock?"+query, 422)
	}
	for _, role := range []string{"purchasing", "accounting", "read_only", "administration"} {
		if _, err := db.ExecContext(ctx, `UPDATE memberships SET role=$1 WHERE id=1`, role); err != nil {
			t.Fatal(err)
		}
		status := 200
		if role == "read_only" {
			status = 403
		}
		read("/stock?alert=any", status)
	}
	noFeature := (&app{db: db, host: "example.com", enableMCP: true, mcpClock: a.mcpClock}).routes()
	invoke(noFeature, "GET", base+"/stock", "", token, false, 404)
	invoke(noFeature, "GET", base+"/stock?alert=any", "", token, false, 404)
	invoke(noFeature, "GET", base+"/stock/1", "", token, false, 404)
	for _, enabled := range []struct{ pos, stock bool }{{true, false}, {false, true}} {
		app := (&app{db: db, host: "example.com", enableMCP: true, enablePOS: enabled.pos, enableStock: enabled.stock, mcpClock: a.mcpClock}).routes()
		invoke(app, "GET", base+"/stock/1", "", token, false, 404)
	}
	invoke(mux, "GET", base+"/stock/1", "", "", false, 401)
	invoke(noFeature, "GET", base+"/stock-alert-summary", "", token, false, 404)
	invoke(noFeature, "GET", base+"/delivery-orders", "", token, false, 404)
	invoke(noFeature, "GET", base+"/delivery-status-summary", "", token, false, 404)
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='read_only' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	read("/knowledge", 200)
	read("/stock", 403)
	read("/stock-alert-summary", 403)
	read("/payables-summary", 403)
	if _, err := db.ExecContext(ctx, `DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1`); err != nil {
		t.Fatal(err)
	}
	read("/knowledge", 404)
	read("/delivery-status-summary", 404)
	read("/stock-alert-summary", 404)
}
