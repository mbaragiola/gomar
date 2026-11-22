package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresMCPStockAdjustmentWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-stock-adjustment-workflow-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'adjustment-workflow',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'purchasing',TRUE);
 INSERT INTO outlet_grants VALUES(1,1);
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(1,1,1,'Synthetic bread',100,TRUE);
 INSERT INTO stock_outlets VALUES(1,TRUE);
 INSERT INTO stock_products VALUES(1,1,TRUE,8);
 INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id) VALUES
 (1,1,1,'recepcion',10,10,'2026-01-01T00:00:00Z','00110000-1234-4123-8123-123456789abc')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := app{mcpClock: func() time.Time { return now }, db: db, host: "example.com", enablePOS: true, enableStock: true, enableMCPStockAdjustmentProposals: true}
	h := a.routes()
	cookies := postgresTestCookies(t, h, "adjustment-workflow", password)
	otherLogin := postgresTestCookies(t, h, "adjustment-workflow", password)
	call := func(method, path, body, token string, login []*http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if login != nil {
			r.Header.Set("Origin", "http://example.com")
			for _, c := range login {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	const browser = "/api/v1/entities/1/outlets/1/"
	const mcp = "/api/v1/mcp/entities/1/outlets/1/"
	const waste = "stock-waste-proposals"
	const count = "stock-physical-count-proposals"
	issue := func(action string) (int64, string) {
		t.Helper()
		w := call("POST", proposalSessionsAuthPath, fmt.Sprintf(`{"entity_id":1,"outlet_id":1,"actions":[%q]}`, action), "", cookies, 201)
		var reply struct {
			ID    int64  `json:"id"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.ID <= 0 || len(reply.Token) != 43 {
			t.Fatal("missing synthetic session", err)
		}
		return reply.ID, reply.Token
	}
	_, wt := issue(stockWasteSessionAction)
	cid, ct := issue(stockPhysicalCountSessionAction)
	b := `{"request_id":"55555555-0000-4000-8000-000000000001","adjustment":{"product_id":1,"quantity":2}}`
	c := strings.Replace(b, `"quantity":2`, `"quantity":0`, 1)
	call("POST", mcp+waste, c, wt, nil, 422)
	call("POST", mcp+count, strings.Replace(c, `,"quantity":0`, "", 1), ct, nil, 422)
	call("POST", mcp+count, c, wt, nil, 401)
	call("POST", mcp+waste, b, ct, nil, 401)
	call("POST", mcp+waste, b, wt, cookies, 422)
	assert := func(rows, balance int64) {
		t.Helper()
		var n, total int64
		if err := db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(delta),0) FROM stock_movements`).Scan(&n, &total); err != nil || n != rows || total != balance {
			t.Fatal(n, total, err)
		}
	}
	submit := func(slug, b, token string) string {
		t.Helper()
		p := call("POST", mcp+slug, b, token, nil, 201)
		replay := call("POST", mcp+slug, b, token, nil, 200)
		if p.Body.String() != replay.Body.String() || strings.Contains(p.Body.String(), "adjustment") || strings.Contains(p.Body.String(), "movement_id") {
			t.Fatal("unsafe receipt")
		}
		var rec stockAdjustmentWireReceipt
		if err := json.Unmarshal(p.Body.Bytes(), &rec); err != nil || rec.ID <= 0 || rec.Status != "pending" {
			t.Fatal(rec, err)
		}
		return fmt.Sprintf("%s%s/%d", browser, slug, rec.ID)
	}
	review := func(path string, before, after, delta int64) string {
		t.Helper()
		var v stockAdjustmentWireDetails
		w := call("GET", path, "", "", cookies, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.Adjustment == nil || v.Adjustment.Before != before || v.Adjustment.After != after || v.Adjustment.Delta != delta || v.Adjustment.LatestMovement <= 0 || v.Adjustment.Revision != 1 || len(v.Digest) != 64 {
			t.Fatal(v, err)
		}
		return fmt.Sprintf(`{"decision":"approve","confirm":true,"digest":%q}`, v.Digest)
	}
	wp := submit(waste, b, wt)
	cp := submit(count, c, ct)
	call("GET", browser+waste, "", "", cookies, 200)
	call("GET", wp, "", "", otherLogin, 404)
	call("GET", strings.Replace(wp, waste, count, 1), "", "", cookies, 404)
	call("POST", mcp+waste, strings.Replace(b, `"quantity":2`, `"quantity":3`, 1), wt, nil, 409)
	wd, cd := review(wp, 10, 8, -2), review(cp, 10, 0, -10)
	call("POST", wp+"/decision", strings.Replace(wd, `"confirm":true`, `"confirm":false`, 1), "", cookies, 422)
	call("POST", wp+"/decision", wd, "", otherLogin, 404)
	assert(1, 10)
	call("POST", wp+"/decision", wd, "", cookies, 200)
	call("POST", wp+"/decision", wd, "", cookies, 409)
	call("POST", cp+"/decision", cd, "", cookies, 409)
	assert(2, 8)
	c = strings.Replace(c, "000000000001", "000000000002", 1)
	cp = submit(count, c, ct)
	cd = review(cp, 8, 0, -8)
	call("POST", cp+"/decision", cd, "", cookies, 200)
	assert(3, 0)
	c = strings.Replace(c, "000000000002", "000000000003", 1)
	cp = submit(count, c, ct)
	cd = review(cp, 0, 0, 0)
	call("DELETE", fmt.Sprintf("%s/%d", proposalSessionsAuthPath, cid), "", "", cookies, 204)
	w := call("GET", cp, "", "", cookies, 200)
	if !strings.Contains(w.Body.String(), `"revoked"`) || strings.Contains(w.Body.String(), "adjustment") {
		t.Fatal("revoked preview was not cleared")
	}
	call("POST", cp+"/decision", cd, "", cookies, 410)
	call("POST", mcp+count, c, ct, nil, 401)
	assert(3, 0)
	b = strings.Replace(b, "000000000001", "000000000004", 1)
	wp = submit(waste, b, wt)
	wd = review(wp, 0, -2, -2)
	call("POST", wp+"/decision", strings.Replace(wd, `"approve"`, `"reject"`, 1), "", cookies, 200)
	assert(3, 0)
	b = strings.Replace(b, "000000000004", "000000000005", 1)
	wp = submit(waste, b, wt)
	wd = review(wp, 0, -2, -2)
	now = now.Add(16 * time.Minute)
	w = call("GET", wp, "", "", cookies, 200)
	if !strings.Contains(w.Body.String(), `"expired"`) || strings.Contains(w.Body.String(), "adjustment") {
		t.Fatal("expired preview was not hidden")
	}
	call("POST", wp+"/decision", wd, "", cookies, 410)
	call("POST", mcp+waste, b, wt, nil, 401)
	assert(3, 0)

}
