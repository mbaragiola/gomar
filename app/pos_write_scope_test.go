package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresPOSWritesUseCurrentScope(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const actor = "synthetic-pos-scope"
	const password = "synthetic pos authorization password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
		VALUES(9101,$1,$2,TRUE,FALSE,FALSE)`, actor, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	run := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO entities(id,name,cuit,active) VALUES(9101,'Synthetic business','00000009101',TRUE),(9102,'Another synthetic business','00000009102',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(9101,9101,'North',TRUE),(9102,9101,'South',TRUE),(9103,9102,'Private',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(9101,9101,9101,'purchasing',TRUE);
		INSERT INTO outlet_grants VALUES(9101,9101),(9101,9102);
		INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(9101,9101,1),(9102,9102,1);
		INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
			VALUES(9101,9101,1,'Synthetic item north',100,TRUE),(9102,9102,1,'Synthetic item south',200,TRUE);
		INSERT INTO stock_outlets VALUES (9101,TRUE);
		INSERT INTO stock_products(outlet_id,product_id,enabled,minimum) VALUES (9101,9101,TRUE,0);`)
	a := &app{db: db, host: "example.com", enablePOS: true, enablePOSTableChecks: true, enableStock: true}
	router := a.routes()
	cookies := postgresTestCookies(t, router, actor, password)
	send := func(ctx context.Context, outlet int, method, suffix, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, method, fmt.Sprintf("http://example.com/api/v1/entities/9101/outlets/%d/pos/%s", outlet, suffix), strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	sale := func(product, request int) string {
		return fmt.Sprintf(`{"id_solicitud":"00000000-0000-4000-8000-%012x","actor_id":9101,"items":[{"producto_id":%d,"revision":1,"cantidad":1}]}`, request, product)
	}
	check := func(table int) string {
		return fmt.Sprintf(`{"id_solicitud":"00000000-0000-4000-8000-%012x","mesa":%d,"items":[{"producto_id":9101,"revision":1,"cantidad":1}]}`, table, table)
	}
	requireStatus := func(t *testing.T, w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("response = %d %s, want %d", w.Code, w.Body, status)
		}
	}
	// The ordinary route, not the test, creates the original receipt and
	// check. A stale write must not create a new row or reveal a replay.
	requireStatus(t, send(ctx, 9101, http.MethodPost, "sales", sale(9101, 1)), http.StatusCreated)
	requireStatus(t, send(ctx, 9101, http.MethodPost, "table-checks", check(7)), http.StatusCreated)
	var checkID string
	if err := db.QueryRowContext(ctx, `SELECT check_id::text FROM pos_table_check_slots WHERE table_number=7`).Scan(&checkID); err != nil {
		t.Fatal(err)
	}
	retire := fmt.Sprintf(`{"id_cuenta":%q,"motivo":"saved_in_error"}`, checkID)
	requireStatus(t, send(ctx, 9103, http.MethodPost, "sales", sale(9101, 2)), http.StatusNotFound)

	cases := []struct {
		name, change, method, suffix, body string
		want                               problem
	}{
		{"sale role", "UPDATE memberships SET role='read_only' WHERE id=9101", http.MethodPost, "sales", sale(9101, 2), forbiddenRole},
		{"sale replay grant", "DELETE FROM outlet_grants WHERE membership_id=9101 AND outlet_id=9101", http.MethodPost, "sales", sale(9101, 1), missingOutlet},
		{"save check membership", "UPDATE memberships SET active=FALSE WHERE id=9101", http.MethodPost, "table-checks", check(8), missingEntity},
		{"retire check branch", "UPDATE outlets SET active=FALSE WHERE id=9101", http.MethodPost, "table-checks/7/retire", retire, missingOutlet},
		{"stock movement role", "UPDATE memberships SET role='read_only' WHERE id=9101", http.MethodPost, "stock/products/9101/movements",
			`{"id_solicitud":"00000000-0000-4000-8000-000000000003","tipo":"recepcion","cantidad":3}`, forbiddenRole},
		{"catalog admin capability", "UPDATE memberships SET role='accounting' WHERE id=9101", http.MethodPut, "catalog/9101/barcode",
			`{"revision":1,"codigo_barras":"00001234"}`, forbiddenRole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset the synthetic scope, without touching any POS records.
			run(`UPDATE memberships SET role='administration',active=TRUE WHERE id=9101;
				UPDATE outlets SET active=TRUE WHERE id=9101;
				INSERT INTO outlet_grants VALUES (9101,9101) ON CONFLICT DO NOTHING;`)
			blocker, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			if err := lockPostgresEntity(ctx, blocker, 9101); err != nil {
				t.Fatal(err)
			}
			if _, err := blocker.ExecContext(ctx, tc.change); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- send(ctx, 9101, tc.method, tc.suffix, tc.body) }()
			waitForPOSFixtureLock(t, ctx, db, blocker)
			if err := blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				requireStatus(t, w, tc.want.status)
				var response map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &response)
				if err != nil || len(response) != 1 || response["detail"] != tc.want.detail {
					t.Fatalf("denial returned unexpected data: %s", w.Body)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
	// No denial changed business data: the original untendered slot and
	// sale survive, only the original sale's one stock movement exists.
	var sales, checks, retirements, slots, moves, revision int
	err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM pos_sales), (SELECT count(*) FROM pos_table_checks),
		(SELECT count(*) FROM pos_table_check_retirements), (SELECT count(*) FROM pos_table_check_slots WHERE table_number=7),
		(SELECT count(*) FROM stock_movements), (SELECT current_revision FROM pos_products WHERE id=9101)`).
		Scan(&sales, &checks, &retirements, &slots, &moves, &revision)
	if err != nil || sales != 1 || checks != 1 || retirements != 0 || slots != 1 || moves != 1 || revision != 1 {
		t.Fatalf("unexpected persisted state: sales=%d checks=%d retirements=%d slots=%d moves=%d revision=%d err=%v",
			sales, checks, retirements, slots, moves, revision, err)
	}

	// An approval that pins the same actor must be able to reach the
	// entity while a sale waits for the actor, not hold a circular wait.
	t.Run("actor before entity", func(t *testing.T) {
		orderCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		approval, err := db.BeginTx(orderCtx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer approval.Rollback()
		var actorID int64
		if err := approval.QueryRowContext(orderCtx, `SELECT id FROM users WHERE id=9101 FOR UPDATE`).Scan(&actorID); err != nil {
			t.Fatal(err)
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- send(orderCtx, 9101, http.MethodPost, "sales", sale(9101, 1)) }()
		waitForPOSFixtureLock(t, orderCtx, db, approval)
		if err := lockPostgresEntity(orderCtx, approval, 9101); err != nil {
			t.Fatal(err)
		}
		if err := approval.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case w := <-done:
			requireStatus(t, w, http.StatusOK)
		case <-orderCtx.Done():
			t.Fatal(orderCtx.Err())
		}
	})

	// Accounting still has its selling role. The outlet lock from one
	// current register must not stop another authorized branch.
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if err := lockPostgresOutlet(ctx, blocker, 9101); err != nil {
		t.Fatal(err)
	}
	north := make(chan *httptest.ResponseRecorder, 1)
	go func() { north <- send(ctx, 9101, http.MethodPost, "sales", sale(9101, 1)) }()
	waitForPOSFixtureLock(t, ctx, db, blocker)
	branchCtx, stop := context.WithTimeout(ctx, 4*time.Second)
	defer stop()
	requireStatus(t, send(branchCtx, 9102, http.MethodPost, "sales", sale(9102, 2)), http.StatusCreated)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-north:
		requireStatus(t, w, http.StatusOK)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func waitForPOSFixtureLock(t *testing.T, ctx context.Context, db *sql.DB, tx *sql.Tx) {
	t.Helper()
	var pid int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		err := db.QueryRowContext(deadline, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.Done():
			t.Fatal("POS write did not reach the expected lock")
		}
	}
}
