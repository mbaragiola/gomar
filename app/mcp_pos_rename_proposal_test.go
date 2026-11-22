package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPOSRenameTypedProposal(t *testing.T) {
	p, err := decodePOSRename([]byte(`{"product_id":1,"expected_revision":2,"name":" New name "}`))
	if err != nil || p != (posRenameInput{1, 2, "New name"}) {
		t.Fatal(p, err)
	}
	for _, raw := range []string{
		`{"product_id":1,"expected_revision":2,"name":"x","price":1}`,
		`{"product_id":1,"expected_revision":2,"name":"x","active":true}`,
		`{"product_id":1,"expected_revision":2,"name":"x","outlet_id":2}`,
		`{"product_id":1,"expected_revision":2,"name":"x","name":"y"}`,
		`{"product_id":1,"expected_revision":2,"Name":"x"}`,
		`{"product_id":1,"expected_revision":9223372036854775807,"name":"x"}`,
		`{"product_id":1,"expected_revision":null,"name":"x"}`,
		`{"product_id":1,"expected_revision":2,"name":"\u0000"}`,
		`{"product_id":1,"expected_revision":2,"name":" "}`,
		"null",
	} {
		if _, err := decodePOSRename([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestPostgresPOSRenameProposalAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-pos-rename"
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
	input := posRenameInput{1, 1, " Crusty bread "}
	prepare := func(app *app, s posRenameScope, input posRenameInput) (posRenameProposal, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		p, err := app.preparePOSRename(ctx, tx, s, input)
		if err != nil {
			return p, err
		}
		return p, tx.Commit()
	}
	expectPrepare := func(app *app, s posRenameScope, input posRenameInput, want error) {
		t.Helper()
		if _, got := prepare(app, s, input); got != want {
			t.Fatalf("prepare got %v, want %v", got, want)
		}
	}
	expectPrepare(&app{db: db}, scope, input, posRenameUnavailable)
	expectPrepare(a, posRenameScope{1, 1, 2}, input, posMissingProduct)
	expectPrepare(a, posRenameScope{1, 1, 3}, input, missingOutlet)
	expectPrepare(a, posRenameScope{2, 1, 1}, input, forbiddenRole)
	expectPrepare(a, scope, posRenameInput{1, 2, "Another"}, posChangedCatalog)
	expectPrepare(a, scope, posRenameInput{1, 1, "Bread"}, posRenameNoChange)
	p, err := prepare(a, scope, input)
	if err != nil || p.Preview.CurrentName != "Bread" || p.Preview.Name != "Crusty bread" || p.Preview.PriceMinor != 200 || !p.Preview.Active {
		t.Fatal(p, err)
	}
	assert := func(rev, price int64, name string, enabled bool) {
		t.Helper()
		var revision, amount, n int64
		var active bool
		var label string
		err := db.QueryRowContext(ctx, `SELECT p.current_revision,v.name,v.price_minor,v.active,
			(SELECT COUNT(*) FROM pos_product_versions WHERE product_id=1)
			FROM pos_products p JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
			WHERE p.id=1`).Scan(&revision, &label, &amount, &active, &n)
		if err != nil || revision != rev || label != name || amount != price || active != enabled || n != rev {
			t.Fatal(revision, label, amount, active, n, err)
		}
		var salesTotal, saleRevision, stock int64
		err = db.QueryRowContext(ctx, `SELECT (SELECT total_minor FROM pos_sales WHERE id=1),
			(SELECT revision FROM pos_sale_items WHERE sale_id=1 AND product_id=1),
			(SELECT coalesce(sum(delta),0) FROM stock_movements WHERE outlet_id=1 AND product_id=1)`).Scan(&salesTotal, &saleRevision, &stock)
		if err != nil || salesTotal != 400 || saleRevision != 1 || stock != 7 {
			t.Fatal(salesTotal, saleRevision, stock, err)
		}
	}
	assert(1, 200, "Bread", true)
	apply := func(s posRenameScope, p posRenameProposal, digest [32]byte, authenticated, commit bool) (posProduct, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return posProduct{}, err
		}
		defer tx.Rollback()
		prod, err := a.applyPOSRename(request("POST", "/synthetic/decision", "", authenticated), tx, user{id: 1}, s, p, digest)
		if err != nil || !commit {
			return prod, err
		}
		return prod, tx.Commit()
	}
	expectApply := func(s posRenameScope, p posRenameProposal, digest [32]byte, authenticated bool, want error) {
		t.Helper()
		if _, got := apply(s, p, digest, authenticated, true); got != want {
			t.Fatalf("apply got %v, want %v", got, want)
		}
	}
	digest := p.digest()
	expectApply(scope, p, [32]byte{}, true, posChangedCatalog)
	expectApply(scope, p, digest, false, unauthenticated)
	expectApply(posRenameScope{1, 1, 2}, p, digest, true, posChangedCatalog)
	expectApply(posRenameScope{2, 1, 1}, p, digest, true, unauthenticated)
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1")
	expectApply(scope, p, digest, true, missingOutlet)
	exec("INSERT INTO outlet_grants VALUES(1,1)")
	exec("UPDATE memberships SET role='purchasing' WHERE id=1")
	expectApply(scope, p, digest, true, forbiddenRole)
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	if _, err := apply(scope, p, digest, true, false); err != nil {
		t.Fatal(err)
	}
	assert(1, 200, "Bread", true)

	native := httptest.NewRecorder()
	router.ServeHTTP(native, request(http.MethodPut, "/api/v1/entities/1/outlets/1/pos/catalog/1",
		`{"nombre":"Bread","precio_centavos":250,"activo":false,"revision":1}`, true))
	if native.Code != 200 {
		t.Fatalf("native update: %d %s", native.Code, native.Body.String())
	}
	expectApply(scope, p, digest, true, posChangedCatalog)
	assert(2, 250, "Bread", false)
	input.Revision = 2
	p, err = prepare(a, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	digest = p.digest()
	var wg sync.WaitGroup
	codes := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := apply(scope, p, digest, true, true); codes <- err })
	}
	wg.Wait()
	close(codes)
	var success, stale int
	for err := range codes {
		switch err {
		case nil:
			success++
		case posChangedCatalog:
			stale++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatal(success, stale)
	}
	assert(3, 250, "Crusty bread", false)
	var originalName string
	if err := db.QueryRowContext(ctx, `SELECT v.name FROM pos_sale_items i JOIN pos_product_versions v
		ON v.product_id=i.product_id AND v.outlet_id=i.outlet_id AND v.revision=i.revision WHERE i.sale_id=1`).Scan(&originalName); err != nil || originalName != "Bread" {
		t.Fatal(originalName, err)
	}
}
