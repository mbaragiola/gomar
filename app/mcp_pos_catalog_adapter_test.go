package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresPOSCatalogChangeAdapters(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-pos-catalog"
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
	r := request(http.MethodPost, "/", "", true)
	prepare := func() (posPriceProposal, posAvailabilityProposal) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		price, err := a.preparePOSPrice(ctx, tx, scope, posPriceInput{1, 1, 350})
		if err != nil || price.PreviousPrice != 200 || price.Name != "Bread" || !price.Active {
			t.Fatal(price, err)
		}
		hide, err := a.preparePOSAvailability(ctx, tx, scope, posAvailabilityInput{1, 1, false})
		if err != nil || !hide.PreviousActive || hide.Input.Active || hide.Price != 200 {
			t.Fatal(hide, err)
		}
		if _, err := a.preparePOSPrice(ctx, tx, posRenameScope{2, 1, 1}, posPriceInput{1, 1, 350}); err != forbiddenRole {
			t.Fatal("role", err)
		}
		if _, err := a.preparePOSAvailability(ctx, tx, posRenameScope{1, 1, 2}, posAvailabilityInput{1, 1, false}); err != posMissingProduct {
			t.Fatal("outlet", err)
		}
		return price, hide
	}
	price, oldHide := prepare()
	check := func(revision, price int64, active bool) {
		t.Helper()
		var rev, cents, gross, historicPrice, stock, moves, historicRevision int64
		var enabled bool
		var name string
		err := db.QueryRowContext(ctx, `SELECT p.current_revision,v.price_minor,v.active,v.name,
 (SELECT total_minor FROM pos_sales WHERE id=1),
 (SELECT revision FROM pos_sale_items WHERE sale_id=1 AND product_id=1),
 (SELECT price_minor FROM pos_product_versions WHERE product_id=1 AND revision=1),
 (SELECT sum(delta) FROM stock_movements WHERE product_id=1),
 (SELECT count(*) FROM stock_movements WHERE product_id=1)
 FROM pos_products p JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
 WHERE p.id=1 AND p.outlet_id=1`).Scan(&rev, &cents, &enabled, &name, &gross, &historicRevision, &historicPrice, &stock, &moves)
		if err != nil || rev != revision || cents != price || enabled != active || name != "Bread" || gross != 400 || historicRevision != 1 || historicPrice != 200 || stock != 7 || moves != 1 {
			t.Fatal(rev, cents, enabled, name, gross, historicRevision, historicPrice, stock, moves, err)
		}
	}
	applyPrice := func(commit bool, r *http.Request) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = a.applyPOSPrice(r, tx, user{id: 1}, scope, price, price.digest())
		if err != nil || !commit {
			return err
		}
		return tx.Commit()
	}
	check(1, 200, true)
	if err := applyPrice(true, request(http.MethodPost, "/", "", false)); err != unauthenticated {
		t.Fatal(err)
	}
	if err := applyPrice(false, r); err != nil {
		t.Fatal(err)
	}
	check(1, 200, true)
	if err := applyPrice(true, r); err != nil {
		t.Fatal(err)
	}
	if err := applyPrice(true, r); err != posChangedCatalog {
		t.Fatal(err)
	}
	check(2, 350, true)
	applyHide := func(hide posAvailabilityProposal, commit bool) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = a.applyPOSAvailability(r, tx, user{id: 1}, scope, hide, hide.digest())
		if err != nil || !commit {
			return err
		}
		return tx.Commit()
	}
	if err := applyHide(oldHide, true); err != posChangedCatalog {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	hide, err := a.preparePOSAvailability(ctx, tx, scope, posAvailabilityInput{1, 2, false})
	tx.Rollback()
	if err != nil || hide.Price != 350 || !hide.PreviousActive {
		t.Fatal(hide, err)
	}
	if err := applyHide(hide, false); err != nil {
		t.Fatal(err)
	}
	check(2, 350, true)
	if err := applyHide(hide, true); err != nil {
		t.Fatal(err)
	}
	check(3, 350, false)
}

func TestDecodePOSCatalogChanges(t *testing.T) {
	start := `{"product_id":1,"expected_revision":2,`
	for _, body := range []string{start + `"price_minor":1}`, start + `"price_minor":100000000}`} {
		if _, err := decodePOSPrice([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{start + `"price_minor":0}`, start + `"price_minor":100000001}`, start + `"price_minor":null}`, start + `"price_minor":123,"active":false}`} {
		if _, err := decodePOSPrice([]byte(body)); err == nil {
			t.Fatal("invalid price accepted")
		}
	}
	if v, err := decodePOSAvailability([]byte(start + `"active":false}`)); err != nil || v.Active {
		t.Fatal(v, err)
	}
	for _, body := range []string{start + `"active":null}`, start + `"active":0}`, start + `"active":false,"active":true}`, start + `"active":false,"price_minor":123}`} {
		if _, err := decodePOSAvailability([]byte(body)); err == nil {
			t.Fatal("invalid availability accepted")
		}
	}
}
