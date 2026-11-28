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

func TestPostgresPOSHistoricalPricePolicy(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-pricing-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
		VALUES(1,'pricing-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO entities(id,name,cuit,active) VALUES(1,'Pricing fixture','00000000000',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',TRUE),(2,1,'Otro local',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2);`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)
	handler := (&app{db: db, host: "example.com", enablePOS: true, enablePOSTableChecks: true, posClock: func() time.Time { return now }}).routes()
	cookies := postgresTestCookies(t, handler, "pricing-fixture", password)
	call := func(method, path, body string, status int) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			req.AddCookie(cookie)
			if cookie.Name == "csrftoken" {
				req.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.Code, status, res.Body)
		}
		return res.Body.String()
	}
	const base = "/api/v1/entities/1/outlets/1/pos"
	call(http.MethodPost, base+"/catalog", `{"nombre":"Pan de prueba","precio_centavos":500,"activo":true}`, 201)
	line := func(revision int) string {
		return fmt.Sprintf(`{"producto_id":1,"revision":%d,"cantidad":2}`, revision)
	}
	checkBody := func(number, revision int) string {
		return fmt.Sprintf(`{"id_solicitud":"55555555-0000-4000-8000-%012d","actor_id":1,"mesa":%d,"items":[%s]}`, number, number, line(revision))
	}
	saleBody := func(number, revision int) string {
		return fmt.Sprintf(`{"id_solicitud":"66666666-0000-4000-8000-%012d","actor_id":1,
			"hora_informada_caja_utc":"2025-11-27T15:00:00Z","items":[%s]}`, number, line(revision))
	}
	receipt := func(body string, total, price int64) {
		t.Helper()
		var sale posSale
		if err := json.Unmarshal([]byte(body), &sale); err != nil || sale.Total != total ||
			len(sale.Items) != 1 || sale.Items[0].Price != price || sale.TillTimeStatus != postime.Review {
			t.Fatalf("unexpected historical sale receipt: %s, %v", body, err)
		}
	}
	rejectCheck := func(number, revision int) {
		t.Helper()
		raw := call(http.MethodPost, base+"/table-checks", checkBody(number, revision), 409)
		var refusal posTableSaveRejection
		if err := json.Unmarshal([]byte(raw), &refusal); err != nil || refusal.Reason != tableSaveCatalog {
			t.Fatalf("expected catalog refusal: %s, %v", raw, err)
		}
	}
	originalCheck := call(http.MethodPost, base+"/table-checks", checkBody(1, 1), 201)
	call(http.MethodPut, base+"/catalog/1", `{"revision":1,"nombre":"Pan de prueba","precio_centavos":900,"activo":true}`, 200)
	originalSale := call(http.MethodPost, base+"/sales", saleBody(1, 1), 201)
	receipt(originalSale, 1000, 500)
	rejectCheck(2, 1)
	call(http.MethodPost, base+"/table-checks", checkBody(3, 2), 201)

	call(http.MethodPut, base+"/catalog/1", `{"revision":2,"nombre":"Pan de prueba","precio_centavos":900,"activo":false}`, 200)
	receipt(call(http.MethodPost, base+"/sales", saleBody(2, 2), 201), 1800, 900)
	for _, revision := range []int{3, 4} {
		if raw := call(http.MethodPost, base+"/sales", saleBody(revision, revision), 422); !strings.Contains(raw, posUnavailablePrice.detail) {
			t.Fatalf("expected unavailable price: %s", raw)
		}
	}
	call(http.MethodPost, "/api/v1/entities/1/outlets/2/pos/sales", saleBody(5, 1), 422)
	rejectCheck(4, 2)
	rejectCheck(5, 3)
	if again := call(http.MethodPost, base+"/sales", saleBody(1, 1), 200); again != originalSale {
		t.Fatalf("historical sale replay changed: %s", again)
	}
	if again := call(http.MethodPost, base+"/table-checks", checkBody(1, 1), 200); again != originalCheck {
		t.Fatalf("saved table check was repriced: %s", again)
	}
	var sales, checks, versions int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM pos_sales),
		(SELECT count(*) FROM pos_table_checks),(SELECT count(*) FROM pos_product_versions WHERE product_id=1 AND outlet_id=1)`).
		Scan(&sales, &checks, &versions); err != nil || sales != 2 || checks != 2 || versions != 3 {
		t.Fatalf("unexpected final sales/checks/versions: %d/%d/%d, %v", sales, checks, versions, err)
	}
}
