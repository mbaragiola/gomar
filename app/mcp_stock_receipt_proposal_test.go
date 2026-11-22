package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestStockReceiptTypedProposal(t *testing.T) {
	for _, good := range []string{`{"product_id":3,"quantity":1}`, `{"product_id":3,"quantity":1000000}`} {
		if _, err := decodeStockReceiptProposal([]byte(good)); err != nil {
			t.Fatal(good, err)
		}
	}
	for _, bad := range []string{
		`{"product_id":0,"quantity":1}`, `{"product_id":3,"quantity":0}`, `{"product_id":3,"quantity":-1}`,
		`{"product_id":3,"quantity":1000001}`, `{"product_id":3,"quantity":1.0}`, `{"product_id":3,"quantity":null}`,
		`{"product_id":3,"quantity":1,"quantity":2}`, `{"product_id":3,"quantity":1,"kind":"merma"}`,
		`{"product_id":3,"quantity":1,"balance":10}`, `{"product_id":3,"quantity":1,"outlet_id":2}`,
		`{"product_id":3,"quantity":1,"request_id":"inject"}`, `{"Product_id":3,"quantity":1}`, "null",
	} {
		if _, err := decodeStockReceiptProposal([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	s := stockReceiptScope{1, 2, 3}
	const id = "88888888-1111-4333-8444-000000000001"
	key := stockReceiptMovementID(s, id)
	if !posRequestID.MatchString(key) || key == id || key != stockReceiptMovementID(s, id) ||
		key == stockReceiptMovementID(stockReceiptScope{1, 2, 4}, id) {
		t.Fatal("movement key is not separated by purpose and scope")
	}
}

func TestPostgresStockReceiptProposalAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-stock-receipt"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'stock-approver',$1,TRUE,FALSE,FALSE),(2,'stock-purchasing',$1,TRUE,FALSE,FALSE),
		(3,'stock-accounting',$1,TRUE,FALSE,FALSE),(4,'stock-reader',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Foreign','00000000002',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Ungrounded',TRUE),(3,2,'Foreign',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'purchasing',TRUE),
			(3,3,1,'accounting',TRUE),(4,4,1,'read_only',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(2,1),(3,1),(4,1);
		INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1),(2,1,1),(3,1,1),(4,2,1);
		INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
			VALUES(1,1,1,'Synthetic bread',100,TRUE),(2,1,1,'Inactive',100,FALSE),(3,1,1,'Untracked',100,TRUE),
			      (4,2,1,'Other outlet',100,TRUE);
		INSERT INTO stock_outlets VALUES(1,TRUE),(2,TRUE);
		INSERT INTO stock_products VALUES(1,1,TRUE,8),(1,2,TRUE,10),(2,4,TRUE,10);`)
	a := &app{db: db, host: "example.com", enablePOS: true, enableStock: true}
	router := a.routes()
	cookies := postgresTestCookies(t, router, "stock-approver", password)
	request := func(authenticated bool) *http.Request {
		r := httptest.NewRequest("POST", "http://example.com/synthetic/decision", nil).WithContext(ctx)
		if authenticated {
			for _, c := range cookies {
				r.AddCookie(c)
			}
		}
		return r
	}
	scope := stockReceiptScope{1, 1, 1}
	input := stockReceiptInput{1, 7}
	const id = "88888888-1111-4333-8444-000000000007"
	prepare := func(application *app, s stockReceiptScope, input stockReceiptInput) (stockReceiptProposal, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		p, err := application.prepareStockReceiptProposal(ctx, tx, s, input, id)
		if err != nil {
			return p, err
		}
		return p, tx.Commit()
	}
	expectPrepare := func(application *app, s stockReceiptScope, input stockReceiptInput, want error) {
		t.Helper()
		if _, got := prepare(application, s, input); got != want {
			t.Fatalf("prepare got %v, want %v", got, want)
		}
	}
	expectPrepare(&app{db: db, enablePOS: true}, scope, input, stockReceiptUnavailable)
	expectPrepare(&app{db: db, enableStock: true}, scope, input, stockReceiptUnavailable)
	expectPrepare(a, stockReceiptScope{1, 1, 2}, input, missingOutlet)
	expectPrepare(a, stockReceiptScope{1, 1, 3}, input, missingOutlet)
	expectPrepare(a, stockReceiptScope{2, 1, 1}, input, nil)
	expectPrepare(a, stockReceiptScope{3, 1, 1}, input, nil)
	expectPrepare(a, stockReceiptScope{4, 1, 1}, input, forbiddenRole)
	for _, product := range []int64{2, 3, 4, 99} {
		expectPrepare(a, scope, stockReceiptInput{product, 7}, stockNotTracked)
	}
	p, err := prepare(a, scope, input)
	if err != nil || p.Preview.ProductName != "Synthetic bread" || p.Preview.ProductRevision != 1 || p.Preview.StockMinimum != 8 {
		t.Fatal(p, err)
	}
	digest := p.digest()
	assert := func(rows, balance int64) {
		t.Helper()
		var n, total, sales, ap int64
		err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM stock_movements),(SELECT COALESCE(SUM(delta),0) FROM stock_movements),
			(SELECT COUNT(*) FROM pos_sales),(SELECT COUNT(*) FROM ap_payments)`).Scan(&n, &total, &sales, &ap)
		if err != nil || n != rows || total != balance || sales != 0 || ap != 0 {
			t.Fatal(n, total, sales, ap, err)
		}
	}
	assert(0, 0)
	apply := func(s stockReceiptScope, p stockReceiptProposal, digest [32]byte, authenticated, commit bool) (stockMovement, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return stockMovement{}, err
		}
		defer tx.Rollback()
		x, err := a.applyStockReceiptProposal(request(authenticated), tx, user{id: 1}, s, p, digest, time.Unix(123, 0))
		if err != nil || !commit {
			return x, err
		}
		return x, tx.Commit()
	}
	expectApply := func(s stockReceiptScope, p stockReceiptProposal, digest [32]byte, authenticated bool, want error) {
		t.Helper()
		if _, got := apply(s, p, digest, authenticated, true); got != want {
			t.Fatalf("apply got %v, want %v", got, want)
		}
	}
	expectApply(scope, p, [32]byte{}, true, stockReceiptStale)
	expectApply(scope, p, digest, false, unauthenticated)
	expectApply(stockReceiptScope{1, 1, 2}, p, digest, true, stockReceiptStale)
	expectApply(stockReceiptScope{2, 1, 1}, p, digest, true, unauthenticated)
	exec("UPDATE stock_outlets SET enabled=FALSE WHERE outlet_id=1")
	expectApply(scope, p, digest, true, stockReceiptStale)
	exec("UPDATE stock_outlets SET enabled=TRUE WHERE outlet_id=1")
	exec("UPDATE stock_products SET enabled=FALSE WHERE outlet_id=1 AND product_id=1")
	expectApply(scope, p, digest, true, stockReceiptStale)
	exec("UPDATE stock_products SET enabled=TRUE,minimum=9 WHERE outlet_id=1 AND product_id=1")
	expectApply(scope, p, digest, true, stockReceiptStale)
	exec("UPDATE stock_products SET minimum=8 WHERE outlet_id=1 AND product_id=1")
	exec("UPDATE memberships SET role='read_only' WHERE id=1")
	expectApply(scope, p, digest, true, forbiddenRole)
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1")
	expectApply(scope, p, digest, true, missingOutlet)
	exec("INSERT INTO outlet_grants VALUES(1,1)")
	exec(`INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(1,1,2,'Renamed bread',100,TRUE);
		UPDATE pos_products SET current_revision=2 WHERE id=1`)
	expectApply(scope, p, digest, true, stockReceiptStale)
	p, err = prepare(a, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	digest = p.digest()

	// This simulates an outer receipt write failure after domain insertion.
	if _, err := apply(scope, p, digest, true, false); err != nil {
		t.Fatal(err)
	}
	assert(0, 0)
	// Native changes to inventory balance do not invalidate an additive
	// receipt, and its later effect is not an absolute count.
	exec(`INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
		VALUES(1,1,1,'recepcion',2,2,'time','88888888-1111-4333-8444-000000000008')`)
	assert(1, 2)
	type outcome struct {
		row stockMovement
		err error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { x, err := apply(scope, p, digest, true, true); results <- outcome{x, err} })
	}
	wg.Wait()
	close(results)
	var movement int64
	for result := range results {
		x := result.row
		if result.err != nil || x.ID <= 0 || x.Kind != "recepcion" || x.Quantity != 7 || x.Delta != 7 || movement != 0 && movement != x.ID {
			t.Fatal(result)
		}
		movement = x.ID
	}
	assert(2, 9)
	var actor, product int64
	var key string
	if err := db.QueryRowContext(ctx, "SELECT actor_id,product_id,request_id FROM stock_movements WHERE id=$1", movement).
		Scan(&actor, &product, &key); err != nil || actor != 1 || product != 1 || key != stockReceiptMovementID(scope, id) {
		t.Fatal(actor, product, err)
	}
}
