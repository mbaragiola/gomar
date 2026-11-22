package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPStockMinimumAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-stock-minimum"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'catalog-editor',$1,TRUE,FALSE,FALSE),(2,'catalog-reader',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Foreign','00000000002',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Second',TRUE),(3,2,'Foreign',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'purchasing',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1);
		INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1),(2,2,1);
		INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
			VALUES(1,1,1,'Bread',200,TRUE),(2,2,1,'Other bread',300,TRUE);
		INSERT INTO pos_sales(id,request_id,outlet_id,actor_id,total_minor,recorded_at)
			VALUES (1,'88888888-1111-4333-8444-000000000009',1,1,400,'2026-11-20T12:00:00Z');
		INSERT INTO pos_sale_items VALUES(1,1,1,1,2);
		INSERT INTO stock_outlets VALUES(1,TRUE);
		INSERT INTO stock_products VALUES(1,1,TRUE,8);
		INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
			VALUES(1,1,1,'recepcion',7,7,'time','88888888-1111-4333-8444-000000000010');`)
	a := &app{db: db, host: "example.com", enablePOS: true, enableStock: true}
	router := a.routes()
	cookies := postgresTestCookies(t, router, "catalog-editor", password)
	request := func(method, path, body string, authenticated bool) *http.Request {
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		return r
	}
	scope := posRenameScope{1, 1, 1}
	zero := int64(0)
	in := stockMinimumInput{1, 1, &zero}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	p, err := a.prepareStockMinimum(ctx, tx, scope, in)
	if err != nil || p.Previous != 8 || p.Minimum != 0 || p.Name != "Bread" {
		t.Fatal(p, err)
	}
	if _, err = a.prepareStockMinimum(ctx, tx, posRenameScope{2, 1, 1}, in); err != forbiddenRole {
		t.Fatal(err)
	}
	if _, err = a.prepareStockMinimum(ctx, tx, posRenameScope{1, 1, 3}, in); err != missingOutlet {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check := func(minimum int64) {
		t.Helper()
		var got, rev, sales, moves, balance int64
		var productEnabled, outletEnabled, active bool
		err := db.QueryRowContext(ctx, `SELECT s.minimum,s.enabled,o.enabled,v.active,p.current_revision,
 (SELECT count(*) FROM pos_sales WHERE outlet_id=1),
 (SELECT count(*) FROM stock_movements WHERE outlet_id=1),
 (SELECT sum(delta) FROM stock_movements WHERE outlet_id=1)
 FROM stock_products s JOIN stock_outlets o ON o.outlet_id=s.outlet_id
 JOIN pos_products p ON p.id=s.product_id AND p.outlet_id=s.outlet_id
 JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
 WHERE s.outlet_id=1 AND s.product_id=1`).Scan(&got, &productEnabled, &outletEnabled, &active, &rev, &sales, &moves, &balance)
		if err != nil || got != minimum || !productEnabled || !outletEnabled || !active || rev != 1 || sales != 1 || moves != 1 || balance != 7 {
			t.Fatal(got, productEnabled, outletEnabled, active, rev, sales, moves, balance, err)
		}
	}
	apply := func(auth, commit bool) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		err = a.applyStockMinimum(request(http.MethodPost, "/", "", auth), tx, user{id: 1}, scope, p, p.digest())
		if err != nil || !commit {
			return err
		}
		return tx.Commit()
	}
	check(8)
	if err = apply(false, true); err != unauthenticated {
		t.Fatal(err)
	}
	if err = apply(true, false); err != nil {
		t.Fatal(err)
	}
	check(8)
	exec("UPDATE stock_products SET minimum=9 WHERE outlet_id=1 AND product_id=1")
	if err = apply(true, true); err != stockMinimumChanged {
		t.Fatal(err)
	}
	check(9)
	exec("UPDATE stock_products SET minimum=8,enabled=FALSE WHERE outlet_id=1 AND product_id=1")
	if err = apply(true, true); err != stockMinimumChanged {
		t.Fatal(err)
	}
	exec("UPDATE stock_products SET enabled=TRUE WHERE outlet_id=1 AND product_id=1")
	if err = apply(true, true); err != nil {
		t.Fatal(err)
	}
	if err = apply(true, true); err != stockMinimumChanged {
		t.Fatal(err)
	}
	check(0)
}

func TestDecodeStockMinimum(t *testing.T) {
	for _, raw := range []string{`{"product_id":1,"expected_revision":1,"minimum":0}`, `{"product_id":1,"expected_revision":1,"minimum":1000000}`} {
		if v, err := decodeStockMinimum([]byte(raw)); err != nil || v.Minimum == nil {
			t.Fatal(v, err)
		}
	}
	for _, raw := range []string{`{"product_id":1,"expected_revision":1,"minimum":null}`, `{"product_id":1,"expected_revision":1}`,
		`{"product_id":1,"expected_revision":1,"minimum":-1}`, `{"product_id":1,"expected_revision":1,"minimum":1000001}`,
		`{"product_id":1,"expected_revision":1,"minimum":0,"enabled":false}`} {
		if _, err := decodeStockMinimum([]byte(raw)); err == nil {
			t.Fatal("invalid stock minimum accepted")
		}
	}
}
