package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostgresStockAdjustmentAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pw = "synthetic-stock-adjustment"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'fixture-adjuster',$1,TRUE,FALSE,FALSE)`, encodePassword(pw))
	exec(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Other',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1);
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(1,1,1,'Synthetic bread',100,TRUE);
 INSERT INTO stock_outlets VALUES(1,TRUE);
 INSERT INTO stock_products VALUES(1,1,TRUE,8);
 INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id) VALUES(1,1,1,'recepcion',10,10,'2026-01-01T00:00:00Z','ff110000-1234-4123-8123-123456789abc')`)
	a := &app{db: db, host: "example.com", enablePOS: true, enableStock: true}
	cookies := postgresTestCookies(t, a.routes(), "fixture-adjuster", pw)
	s := stockReceiptScope{1, 1, 1}
	const wasteID = "aabb3344-1234-4123-8123-123456789abc"
	const countID = "aabb3344-1234-4123-8123-123456789abd"
	zero := int64(0)
	waste := func(scope stockReceiptScope, n int64) (stockAdjustmentProposal, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return a.prepareStockWaste(ctx, tx, scope, stockWasteInput{1, n}, wasteID)
	}
	count := func(n *int64) (stockAdjustmentProposal, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return a.prepareStockPhysicalCount(ctx, tx, s, stockPhysicalCountInput{1, n}, countID)
	}
	apply := func(p stockAdjustmentProposal, commit, authenticated bool) (stockMovement, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return stockMovement{}, err
		}
		defer tx.Rollback()
		r := httptest.NewRequest("POST", "http://example.com/synthetic/decision", nil).WithContext(ctx)
		if authenticated {
			for _, c := range cookies {
				r.AddCookie(c)
			}
		}
		row, err := a.applyStockAdjustment(r, tx, user{id: 1}, s, p, p.digest(), time.Unix(123, 0))
		if err == nil && commit {
			err = tx.Commit()
		}
		return row, err
	}
	assert := func(rows, balance int64) {
		t.Helper()
		var n, total int64
		if err := db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(delta),0) FROM stock_movements`).Scan(&n, &total); err != nil || n != rows || total != balance {
			t.Fatal(n, total, err)
		}
	}
	for _, n := range []int64{-1, 0, stockQuantityMax + 1} {
		if _, e := waste(s, n); e != badInput {
			t.Fatal(e)
		}
	}
	if _, e := count(nil); e != badInput {
		t.Fatal(e)
	}
	if _, e := waste(stockReceiptScope{1, 1, 2}, 2); e != missingOutlet {
		t.Fatal(e)
	}
	p, e := count(&zero)
	if e != nil || p.Preview.Before != 10 || p.Preview.After != 0 || p.Preview.Delta != -10 {
		t.Fatal(p, e)
	}
	assert(1, 10)
	if _, e := apply(p, true, false); e != unauthenticated {
		t.Fatal(e)
	}
	// A native zero-net change must invalidate the observed count.
	exec(`INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id) VALUES
 (1,1,1,'recepcion',1,1,'2026-01-02T00:00:00Z','ff110000-1234-4123-8123-123456789abd'),(1,1,1,'merma',1,-1,'2026-01-02T00:00:00Z','ff110000-1234-4123-8123-123456789abe')`)
	if _, e := apply(p, true, true); e != stockAdjustmentStale {
		t.Fatal(e)
	}
	assert(3, 10)
	p, e = count(&zero)
	if e != nil {
		t.Fatal(e)
	}
	w, e := waste(s, 2)
	if e != nil || w.Preview.Before != 10 || w.Preview.After != 8 || w.Preview.Delta != -2 {
		t.Fatal(w, e)
	}
	if _, e := apply(w, false, true); e != nil {
		t.Fatal(e)
	}
	assert(3, 10)
	exec(`UPDATE memberships SET role='read_only' WHERE id=1`)
	if _, e := apply(w, true, true); e != forbiddenRole {
		t.Fatal(e)
	}
	exec(`UPDATE memberships SET role='administration' WHERE id=1`)
	type result struct {
		row stockMovement
		err error
	}
	c := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { r, e := apply(w, true, true); c <- result{r, e} }()
	}
	first, second := <-c, <-c
	if first.err != nil || second.err != nil || first.row.ID <= 0 || second.row.ID != first.row.ID || first.row.Delta != -2 {
		t.Fatal(first, second)
	}
	assert(4, 8)
	if _, e := apply(p, true, true); e != stockAdjustmentStale {
		t.Fatal(e)
	}
	p, e = count(&zero)
	if e != nil {
		t.Fatal(e)
	}
	if row, e := apply(p, true, true); e != nil || row.Kind != "conteo" || row.Delta != -8 {
		t.Fatal(row, e)
	}
	assert(5, 0)
}
