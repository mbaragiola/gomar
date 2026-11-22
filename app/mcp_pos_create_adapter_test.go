package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodePOSCreate(t *testing.T) {
	for _, raw := range []string{
		`{"name":"Bread","price_minor":1}`,
		`{"name":"Bread","price_minor":1,"active":null}`,
		`{"name":"Bread","price_minor":1.5,"active":false}`,
		`{"name":"Bread","price_minor":0,"active":false}`,
		`{"name":"Bread","price_minor":100000001,"active":true}`,
		`{"name":"Bread","price_minor":1,"active":true,"active":false}`,
		`{"name":"Bread","price_minor":1,"active":false,"stock":1}`,
		`{"name":" ","price_minor":1,"active":true}`,
		`{"name":"` + strings.Repeat("á", 121) + `","price_minor":1,"active":false}`,
	} {
		if _, err := decodePOSCreate([]byte(raw)); err != badInput {
			t.Fatal(raw, err)
		}
	}
	p, err := decodePOSCreate([]byte(`{"name":"  Café  ","price_minor":100000000,"active":false}`))
	if err != nil || p != (posCreateInput{"Café", posPriceMax, false}) {
		t.Fatal(p, err)
	}
}

func TestPostgresPOSCreateAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-catalog-create"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'create-editor',$1,TRUE,FALSE,FALSE)`, encodePassword(password))
	exec(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Other',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
		INSERT INTO outlet_grants VALUES(1,1);
		INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1001,1,1);
		INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(1001,1,1,'Bread',200,TRUE);
		INSERT INTO pos_sales(id,request_id,outlet_id,actor_id,total_minor,recorded_at) VALUES(1,'88888888-1111-4333-8444-000000000009',1,1,400,'2026-11-20T12:00:00Z');
		INSERT INTO pos_sale_items VALUES(1,1,1001,1,2);
		INSERT INTO stock_outlets VALUES(1,TRUE);
		INSERT INTO stock_products VALUES(1,1001,TRUE,8);
		INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
		 VALUES(1,1001,1,'recepcion',7,7,'time','88888888-1111-4333-8444-000000000010');`)
	a := &app{db: db, host: "example.com", enablePOS: true, enableStock: true}
	cookies := postgresTestCookies(t, a.routes(), "create-editor", password)
	s := posRenameScope{1, 1, 1}
	in := posCreateInput{" Café ", 167, false}
	prepare := func(s posRenameScope) (posCreateProposal, error) {
		t.Helper()
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		return a.preparePOSCreate(ctx, tx, s, in)
	}
	apply := func(p posCreateProposal, digest [32]byte, login, commit bool) (posProduct, error) {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return posProduct{}, e
		}
		defer tx.Rollback()
		r := httptest.NewRequestWithContext(ctx, "POST", "http://example.com/", nil)
		if login {
			for _, c := range cookies {
				r.AddCookie(c)
			}
		}
		v, e := a.applyPOSCreate(r, tx, user{id: 1}, s, p, digest)
		if e == nil && commit {
			e = tx.Commit()
		}
		return v, e
	}
	check := func(want int) {
		t.Helper()
		var count, oldPrice, sales, gross, stock, tracking, version int
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM pos_products WHERE outlet_id=1),
			(SELECT price_minor FROM pos_product_versions WHERE product_id=1001 AND revision=1),
			(SELECT count(*) FROM pos_sales),(SELECT total_minor FROM pos_sales WHERE id=1),
			(SELECT sum(delta) FROM stock_movements),(SELECT count(*) FROM stock_products),
			(SELECT revision FROM pos_sale_items WHERE sale_id=1)`).Scan(&count, &oldPrice, &sales, &gross, &stock, &tracking, &version); err != nil ||
			count != want || oldPrice != 200 || sales != 1 || gross != 400 || stock != 7 || tracking != 1 || version != 1 {
			t.Fatal(count, oldPrice, sales, gross, stock, tracking, version, err)
		}
	}
	if _, e := prepare(posRenameScope{1, 1, 2}); e != missingOutlet {
		t.Fatal(e)
	}
	p, e := prepare(s)
	if e != nil || p.Preview.Existing != 1 || p.Preview.Limit != posCatalogMax || p.Preview.Currency != "ARS" || p.Preview.Name != "Café" || p.Preview.Active {
		t.Fatal(p, e)
	}
	check(1)
	if _, e := apply(p, [32]byte{}, true, true); e != posChangedCatalog {
		t.Fatal(e)
	}
	if _, e := apply(p, p.digest(), false, true); e != unauthenticated {
		t.Fatal(e)
	}
	exec(`UPDATE memberships SET role='purchasing' WHERE id=1`)
	if _, e := apply(p, p.digest(), true, true); e != forbiddenRole {
		t.Fatal(e)
	}
	exec(`UPDATE memberships SET role='administration' WHERE id=1; DELETE FROM outlet_grants WHERE membership_id=1`)
	if _, e := apply(p, p.digest(), true, true); e != missingOutlet {
		t.Fatal(e)
	}
	exec(`INSERT INTO outlet_grants VALUES(1,1)`)
	if _, e := apply(p, p.digest(), true, false); e != nil {
		t.Fatal(e)
	}
	check(1)
	type outcome struct {
		result posProduct
		err    error
	}
	out := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() { v, e := apply(p, p.digest(), true, true); out <- outcome{v, e} }()
	}
	first, second := <-out, <-out
	if first.err != nil {
		first, second = second, first
	}
	if first.err != nil || second.err != posCreateCapacityChanged || first.result.ID <= 0 || first.result.Revision != 1 ||
		first.result.Price != 167 || first.result.Name != "Café" || first.result.Active {
		t.Fatal(first, second)
	}
	check(2)
	exec(`INSERT INTO pos_products(id,outlet_id,current_revision) SELECT i,1,1 FROM generate_series(2001,2498) i;
	 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
	 SELECT i,1,1,'Synthetic item',1,TRUE FROM generate_series(2001,2498) i`)
	check(500)
	if _, e := prepare(s); e != posCreateCapacity {
		t.Fatal(e)
	}
	if _, e := apply(p, p.digest(), true, true); e != posCreateCapacityChanged {
		t.Fatal(e)
	}
}
