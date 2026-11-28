package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mbaragiola/gomar/app/internal/postime"
)

func TestPostgresPOSSaleTimeAcrossLocalMonthAndScopedReplay(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-Caja-clock-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'clock-cashier',$1,TRUE,FALSE,FALSE),(2,'other-cashier',$1,TRUE,FALSE,FALSE),
		(3,'clock-reader',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO entities(id,name,cuit,active) VALUES (1,'Prueba','00000000001',TRUE),(2,'Ajena','00000000002',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES (1,1,'Centro',TRUE),(2,1,'Otra caja',TRUE),(3,2,'Ajeno',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES
		 (1,1,1,'administration',TRUE),(2,2,1,'administration',TRUE),(3,3,1,'read_only',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1),(3,1);
		INSERT INTO stock_outlets VALUES(1,TRUE);
	`); err != nil {
		t.Fatal(err)
	}
	registered := time.Date(2026, 12, 1, 3, 1, 0, 0, time.UTC)
	app := &app{db: db, host: "example.com", enablePOS: true, enableStock: true, enableMCP: true,
		posClock: func() time.Time { return registered }, mcpClock: func() time.Time { return registered }}
	handler := app.routes()
	cashier := postgresTestCookies(t, handler, "clock-cashier", password)
	other := postgresTestCookies(t, handler, "other-cashier", password)
	reader := postgresTestCookies(t, handler, "clock-reader", password)
	api := func(method, path, body string, session []*http.Cookie, token string, status int) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for _, c := range session {
			req.AddCookie(c)
			if c.Name == "csrftoken" {
				req.Header.Set("X-CSRFToken", c.Value)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: HTTP %d (want %d), cache %q, body %s", method, path, rec.Code, status, rec.Header().Get("Cache-Control"), rec.Body)
		}
		if status >= 400 && (strings.Contains(rec.Body.String(), "02:58") || strings.Contains(rec.Body.String(), "03:07") ||
			strings.Contains(rec.Body.String(), "handoff_url")) {
			t.Fatalf("denial disclosed evidence: %s", rec.Body)
		}
		return rec.Body.String()
	}
	token := func(cookies []*http.Cookie, scope string) string {
		t.Helper()
		var response struct{ Token string }
		body := api(http.MethodPost, "/api/v1/auth/mcp-token", scope, cookies, "", 201)
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.Token == "" {
			t.Fatal("did not issue synthetic token", err)
		}
		return response.Token
	}
	const native = "/api/v1/entities/1/outlets/1/pos"
	const mcpBase = "/api/v1/mcp/entities/1/outlets/1/pos-sale-times/"
	const saleA = "92000000-0000-4000-8000-000000000001"
	const saleB = "92000000-0000-4000-8000-000000000002"
	const claimedA = "2026-12-01T02:58:00Z"
	const recordedA = "2026-12-01T03:07:00Z"
	const bothB = "2026-12-01T03:01:00Z"
	catalog := api(http.MethodGet, native+"/catalog", "", cashier, "", 200)
	if !strings.Contains(catalog, `"hora_informada_caja_utc_v1":true`) {
		t.Fatal("cashier could not negotiate till-clock evidence")
	}
	if strings.Contains(api(http.MethodGet, native+"/catalog", "", reader, "", 200), "hora_informada_caja_utc_v1") {
		t.Fatal("read-only role saw seller capability")
	}
	api(http.MethodPost, native+"/catalog", `{"nombre":"Pan de prueba","precio_centavos":300000,"activo":true}`, cashier, "", 201)
	if _, err := db.ExecContext(ctx, `INSERT INTO stock_products VALUES(1,1,TRUE,0)`); err != nil {
		t.Fatal(err)
	}
	body := func(id string, actor, qty int, timeField string) string {
		return fmt.Sprintf(`{"id_solicitud":%q,"actor_id":%d,"items":[{"producto_id":1,"revision":1,"cantidad":%d}],"metodo_pago":"efectivo"%s}`, id, actor, qty, timeField)
	}
	claim := func(instant string) string { return fmt.Sprintf(`,"hora_informada_caja_utc":%q`, instant) }
	sale := func(req string, actor, qty int, extra string, cookies []*http.Cookie, status int) string {
		t.Helper()
		return api(http.MethodPost, native+"/sales", body(req, actor, qty, extra), cookies, "", status)
	}

	b := sale(saleB, 1, 1, claim(bothB), cashier, 201)
	registered = registered.Add(6 * time.Minute)
	a := sale(saleA, 1, 1, claim(claimedA), cashier, 201)
	registered = registered.Add(3 * time.Hour) // The retry occurs after the first response is lost.
	for _, alternative := range []string{claim(claimedA), claim("2026-12-20T02:58:00Z"), "", `,"hora_informada_caja_utc":{}`} {
		if replay := sale(saleA, 1, 1, alternative, cashier, 200); replay != a {
			t.Fatalf("changed the original receipt on an economic replay: %s", replay)
		}
	}
	sale(saleA, 1, 2, claim(claimedA), cashier, 409)
	sale(saleA, 1, 1, claim(claimedA), other, 403)
	sale(saleA, 2, 1, claim(claimedA), other, 409)
	api(http.MethodPost, "/api/v1/entities/1/outlets/2/pos/sales", body(saleA, 1, 1, claim(claimedA)), cashier, "", 409)

	for _, wanted := range []struct{ response, claim, registered string }{{a, claimedA, recordedA}, {b, bothB, bothB}} {
		var receipt posSale
		if err := json.Unmarshal([]byte(wanted.response), &receipt); err != nil || receipt.Recorded != wanted.registered ||
			receipt.TillReported != wanted.claim || receipt.TillTimeStatus != postime.Unverified || receipt.Total != 300000 {
			t.Fatalf("unexpected receipt clocks: %+v, %v", receipt, err)
		}
	}
	creds := token(cashier, `{"entity_id":1,"outlet_id":1}`)
	const recoveryPrefix = "/sales/"
	recovery := native + recoveryPrefix + saleA
	var receipt struct {
		posSale
		Actor int64 `json:"actor_id"`
	}
	wireReceipt := api(http.MethodGet, recovery, "", cashier, "", 200)
	if err := json.Unmarshal([]byte(wireReceipt), &receipt); err != nil || receipt.RequestID != saleA ||
		receipt.ID < 1 || receipt.Actor != 1 || receipt.Recorded != recordedA || receipt.TillReported != claimedA ||
		receipt.Total != 300000 || receipt.PaymentMethod != "efectivo" || len(receipt.Items) != 1 ||
		receipt.Items[0].Quantity != 1 || receipt.Items[0].ProductID < 1 || receipt.Items[0].Revision != 1 ||
		strings.Contains(wireReceipt, "precio_centavos") || strings.Contains(wireReceipt, `"nombre"`) {
		t.Fatalf("unexpected scoped recovery receipt: %s, %v", wireReceipt, err)
	}
	api(http.MethodGet, recovery+"?extra=1", "", cashier, "", 422)
	api(http.MethodGet, recovery, "", reader, "", 403)
	api(http.MethodGet, recovery, "", other, "", 404)
	api(http.MethodGet, recovery, "", nil, "", 401)
	api(http.MethodGet, recovery, "", nil, creds, 401)
	api(http.MethodGet, "/api/v1/entities/1/outlets/2/pos"+recoveryPrefix+saleA, "", cashier, "", 404)
	api(http.MethodGet, "/api/v1/entities/2/outlets/3/pos"+recoveryPrefix+saleA, "", cashier, "", 404)

	nativeA := api(http.MethodGet, native+"/sale-times/"+saleA, "", cashier, "", 200)
	if mcpA := api(http.MethodGet, mcpBase+saleA, "", nil, creds, 200); mcpA != nativeA {
		t.Fatalf("timing differs across surfaces: %s / %s", nativeA, mcpA)
	}
	var evidence postime.Evidence
	if err := json.Unmarshal([]byte(nativeA), &evidence); err != nil ||
		evidence != (postime.Evidence{RequestID: saleA, RegisteredAt: recordedA, TillReportedAt: claimedA,
			TillTimeStatus: postime.Unverified, HandoffURL: "/pos#entity=1&outlet=1&venta=" + saleA}) {
		t.Fatalf("unexpected timing handoff: %+v, %v", evidence, err)
	}
	api(http.MethodGet, native+"/sale-times/"+saleA, "", other, "", 404)
	api(http.MethodGet, native+"/sale-times/"+saleA, "", reader, "", 403)
	api(http.MethodGet, native+"/sale-times/"+saleA, "", nil, "", 401)
	api(http.MethodGet, "/api/v1/entities/1/outlets/2/pos/sale-times/"+saleA, "", cashier, "", 404)
	api(http.MethodGet, "/api/v1/entities/2/outlets/3/pos/sale-times/"+saleA, "", cashier, "", 404)
	api(http.MethodGet, mcpBase+saleA, "", nil, token(other, `{"entity_id":1,"outlet_id":1}`), 404)
	api(http.MethodGet, mcpBase+saleA, "", nil, token(reader, `{"entity_id":1,"outlet_id":1}`), 403)
	api(http.MethodGet, mcpBase+saleA, "", nil, token(cashier, `{"entity_id":1}`), 404)
	api(http.MethodGet, "/api/v1/mcp/entities/1/outlets/2/pos-sale-times/"+saleA, "", nil, creds, 404)
	api(http.MethodGet, "/api/v1/mcp/entities/2/outlets/1/pos-sale-times/"+saleA, "", nil, creds, 404)

	summary := func(period string, count, amount int64) {
		t.Helper()
		result := api(http.MethodGet, "/api/v1/mcp/entities/1/sales-summary?period="+period, "", nil, creds, 200)
		var parsed mcpSales
		if err := json.Unmarshal([]byte(result), &parsed); err != nil || parsed.Currency != "ARS" ||
			parsed.Timezone != "America/Argentina/Buenos_Aires" || parsed.SaleCount != count || parsed.TotalMinor != amount {
			t.Fatalf("unexpected %s receipt-month totals: %s", period, result)
		}
	}
	summary("this_month", 2, 600000)
	summary("previous_month", 0, 0)
	var saleCount, items, moves, stockDelta int
	var stockTimes string
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM pos_sales),
		(SELECT count(*) FROM pos_sale_items),count(*),COALESCE(sum(delta),0),
		string_agg(recorded_at,',' ORDER BY recorded_at)
		FROM stock_movements WHERE outlet_id=1 AND kind='venta'`).Scan(&saleCount, &items, &moves, &stockDelta, &stockTimes); err != nil ||
		saleCount != 2 || items != 2 || moves != 2 || stockDelta != -2 || stockTimes != bothB+","+recordedA {
		t.Fatalf("retry or reported time changed immutable stock: sales=%d items=%d moves=%d delta=%d times=%q err=%v", saleCount, items, moves, stockDelta, stockTimes, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE pos_sales SET till_reported_at_utc=NULL WHERE request_id=$1`, saleA); err == nil {
		t.Fatal("database allowed overwriting first till evidence")
	}

	const legacy = "92000000-0000-4000-8000-000000000003"
	const skewed = "92000000-0000-4000-8000-000000000004"
	sale(legacy, 1, 1, claim("2026-11-31T02:58:00Z"), cashier, 422)
	first := sale(legacy, 1, 1, "", cashier, 201)
	if replay := sale(legacy, 1, 1, claim(claimedA), cashier, 200); replay != first || strings.Contains(replay, "hora_informada_caja_utc") {
		t.Fatalf("backfilled evidence on a legacy replay: %s", replay)
	}
	if result := api(http.MethodGet, mcpBase+legacy, "", nil, creds, 200); !strings.Contains(result, `"till_time_status":"not_reported"`) || strings.Contains(result, "till_reported_at_utc") {
		t.Fatalf("legacy evidence fabricated: %s", result)
	}
	if result := sale(skewed, 1, 1, claim("2099-01-01T03:07:00Z"), cashier, 201); !strings.Contains(result, `"estado_hora_caja":"review"`) {
		t.Fatalf("parseable implausible clock should be accepted for review: %s", result)
	}

	// The issued token and a browser session cannot keep access after a scope change.
	if _, err := db.ExecContext(ctx, `DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1`); err != nil {
		t.Fatal(err)
	}
	api(http.MethodGet, native+"/sale-times/"+saleA, "", cashier, "", 404)
	api(http.MethodGet, mcpBase+saleA, "", nil, creds, 404)
}

func TestPostgresPOSSaleTimeMigrationPreservesLegacyRegistration(t *testing.T) {
	ctx, db := testPostgresDB(t)
	const prior = 38
	if len(postgresMigrationNames) <= prior || postgresMigrationNames[prior] != "039_pos_sale_till_time.sql" {
		t.Fatal("unexpected sale-time migration position")
	}
	if err := migratePostgresThrough(ctx, db, postgresMigrationNames[:prior]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO users(id,username,password,active,staff,superuser) VALUES(9,'legacy-clock','synthetic',TRUE,FALSE,FALSE);
		INSERT INTO entities(id,name,cuit,active) VALUES(9,'Prueba','00000000009',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(9,9,'Centro',TRUE);
		INSERT INTO pos_sales(id,request_id,outlet_id,actor_id,total_minor,recorded_at)
			VALUES (9,'92000000-0000-4000-8000-000000000009',9,9,600,'2026-11-30T03:07:00.123456789Z');
	`); err != nil {
		t.Fatal(err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	var recorded string
	var price int64
	var missing bool
	if err := db.QueryRowContext(ctx, `SELECT recorded_at,total_minor,till_reported_at_utc IS NULL FROM pos_sales WHERE id=9`).
		Scan(&recorded, &price, &missing); err != nil || !missing || recorded != "2026-11-30T03:07:00.123456789Z" || price != 600 {
		t.Fatalf("migration changed legacy sale: %q, %v, %v, %v", recorded, price, missing, err)
	}
}
