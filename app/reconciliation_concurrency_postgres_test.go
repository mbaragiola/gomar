package main

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresReconciliationConcurrentOrganizations(t *testing.T) {
	ctx, db := testPostgresDB(t)
	var hasSchema bool
	if err := db.QueryRowContext(ctx, "SELECT to_regnamespace('gomar') IS NOT NULL").Scan(&hasSchema); err != nil || hasSchema {
		t.Fatalf("requires an empty QA database: schema present=%t: %v", hasSchema, err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "reconciliation-fixture-only"
	for id, name := range []string{"fixture-centro", "fixture-sur"} {
		key := id + 1
		for _, cmd := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO users(id,username,password,active,staff,superuser) VALUES ($1,$2,$3,true,false,false)`, []any{key, name, encodePassword(password)}},
			{`INSERT INTO entities(id,name,cuit,active) VALUES ($1,$2,$3,true)`, []any{key, name, fmt.Sprintf("%011d", key)}},
			{`INSERT INTO outlets(id,entity_id,name,active) VALUES ($1,$1,$2,true)`, []any{key, name}},
			{`INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES ($1,$1,$1,'administration',true)`, []any{key}},
			{`INSERT INTO outlet_grants(membership_id,outlet_id) VALUES ($1,$1)`, []any{key}},
		} {
			if _, err := db.ExecContext(ctx, cmd.sql, cmd.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	handler := (&app{db: db, host: "example.com"}).routes()
	cookies := [][]*http.Cookie{
		nil,
		postgresTestCookies(t, handler, "fixture-centro", password),
		postgresTestCookies(t, handler, "fixture-sur", password),
	}
	invoke := func(org int, req *http.Request) *httptest.ResponseRecorder {
		for _, cookie := range cookies[org] {
			req.AddCookie(cookie)
			if cookie.Name == "csrftoken" {
				req.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	newRun := func(org int) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/entities/%d/reconciliations", org),
			strings.NewReader(fmt.Sprintf(`{"outlet_ids":[%d],"match_reference":true}`, org)))
		req.Header.Set("Content-Type", "application/json")
		rec := invoke(org, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create run: %d %s", rec.Code, rec.Body)
		}
		var row struct {
			ID int `json:"id"`
		}
		decodeReconciliationFixture(t, rec.Body.Bytes(), &row)
		if row.ID <= 0 {
			t.Fatal("missing run ID")
		}
		return row.ID
	}
	upload := func(org, run, rows int) *http.Request {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for name, value := range map[string]string{
			"mapping": `{"date":0,"amount":1,"reference":2}`, "date_format": "%Y-%m-%d", "default_currency": "ARS",
			"default_outlet_id": fmt.Sprint(org), "side": "payments", "expected_revision": "1", "operation_key": "test-import",
		} {
			if err := form.WriteField(name, value); err != nil {
				t.Fatal(err)
			}
		}
		csv, err := form.CreateFormFile("file", "synthetic.csv")
		if err != nil {
			t.Fatal(err)
		}
		buf := bytes.NewBufferString("fecha,importe,referencia\n")
		for i := range rows {
			fmt.Fprintf(buf, "2026-11-01,10.00,fixture-%04d\n", i)
		}
		if _, err := csv.Write(buf.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/entities/%d/reconciliations/%d/sources", org, run), &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		return req
	}
	type result struct {
		code    int
		elapsed time.Duration
	}
	timed := func(org int, req *http.Request) result {
		start := time.Now()
		rec := invoke(org, req)
		return result{rec.Code, time.Since(start)}
	}
	check := func(got result, code int) {
		t.Helper()
		if got.code != code {
			t.Fatalf("import status=%d, want %d", got.code, code)
		}
	}
	const batch = 1000
	serial := timed(1, upload(1, newRun(1), batch))
	check(serial, http.StatusCreated)
	runA, runB := newRun(1), newRun(2)
	reqA, reqB := upload(1, runA, batch), upload(2, runB, batch)
	start := make(chan struct{})
	a, b := make(chan result, 1), make(chan result, 1)
	go func() { <-start; a <- timed(1, reqA) }()
	go func() { <-start; b <- timed(2, reqB) }()
	close(start)
	var gotA, gotB result
	var maxWaitMicros int64
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	waitA, waitB := (<-chan result)(a), (<-chan result)(b)
	for waitA != nil || waitB != nil {
		select {
		case gotA = <-waitA:
			waitA = nil
		case gotB = <-waitB:
			waitB = nil
		case <-ticker.C:
			var micros int64
			if err := db.QueryRowContext(ctx, `SELECT coalesce(max(
				(extract(epoch FROM clock_timestamp() - l.waitstart) * 1000000)::bigint), 0)
				FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
				WHERE NOT l.granted AND a.datname=current_database()
				AND a.query LIKE '%reconciliation_source_highwater%FOR UPDATE%'`).Scan(&micros); err != nil {
				t.Fatal(err)
			}
			maxWaitMicros = max(maxWaitMicros, micros)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	check(gotA, http.StatusCreated)
	check(gotB, http.StatusCreated)
	t.Logf("ordinary %d-row import serial=%s concurrent centro=%s sur=%s longest sampled counter wait=%s",
		batch, serial.elapsed, gotA.elapsed, gotB.elapsed, time.Duration(maxWaitMicros)*time.Microsecond)
	sourceIDs := make(map[int]bool)
	for i, run := range []int{runA, runB} {
		var count, minID, maxID, sourceID, revision, events int
		err := db.QueryRowContext(ctx, `SELECT count(*), min(d.id), max(d.id), s.id, r.revision,
			(SELECT count(*) FROM reconciliation_detail_events e WHERE e.run_id=r.id
			 AND e.actor_id=$2 AND e.payload::jsonb->>'action'='import')
			FROM reconciliation_read_runs r
			JOIN reconciliation_read_sources s ON s.run_id=r.id
			JOIN reconciliation_detail_rows d ON d.run_id=r.id AND d.source_id=s.id AND d.outlet_id=$2
			WHERE r.id=$1 AND r.entity_id=$2 GROUP BY r.id,s.id`, run, i+1).
			Scan(&count, &minID, &maxID, &sourceID, &revision, &events)
		if err != nil || count != batch || sourceID < 2 || sourceID > 3 || sourceIDs[sourceID] ||
			minID != (sourceID-1)*batch+1 || maxID != sourceID*batch || revision != 2 || events != 1 {
			t.Fatalf("run %d: count=%d rows=%d–%d source=%d revision=%d events=%d err=%v",
				run, count, minID, maxID, sourceID, revision, events, err)
		}
		sourceIDs[sourceID] = true
	}

	// Pause one ordinary import after allocation and let a second tenant reach
	// the shared counter. Abort the first import after releasing the test lock.
	failRun, nextRun := newRun(1), newRun(2)
	const gate = 375853512
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION gomar.fixture_pause_import() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(%d);
			RAISE EXCEPTION 'fictional import failure';
		END $$;
		CREATE TRIGGER fixture_pause_import AFTER INSERT ON reconciliation_detail_rows
		FOR EACH ROW WHEN (NEW.run_id=%d AND NEW.record_number>2)
		EXECUTE FUNCTION gomar.fixture_pause_import();`, gate, failRun)); err != nil {
		t.Fatal(err)
	}
	lock, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, gate); err != nil {
		t.Fatal(err)
	}
	var holder int
	if err := lock.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	waitFor := func(query string, values ...any) int {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var pid int
			if err := db.QueryRowContext(ctx, query, values...).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if pid > 0 {
				return pid
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatal("fixture did not reach its expected PostgreSQL lock")
		return 0
	}
	const blocker = `SELECT COALESCE(min(pid),0) FROM pg_stat_activity
	WHERE datname=current_database() AND $1 = ANY(pg_blocking_pids(pid))`
	reqA, reqB = upload(1, failRun, 2), upload(2, nextRun, 2)
	go func() { a <- timed(1, reqA) }()
	blockedA := waitFor(blocker, holder)
	waitStarted := time.Now()
	go func() { b <- timed(2, reqB) }()
	_ = waitFor(blocker+` AND query LIKE '%reconciliation_source_highwater%FOR UPDATE%'`, blockedA)
	waitObserved := time.Since(waitStarted)
	if err := lock.Commit(); err != nil {
		t.Fatal(err)
	}
	gotA, gotB = <-a, <-b
	check(gotA, http.StatusInternalServerError)
	check(gotB, http.StatusCreated)
	t.Logf("controlled cross-organization counter wait observed after %s; request elapsed=%s", waitObserved, gotB.elapsed)
	var sources, rows, version, events, sourceID, firstRow, lastRow int
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM reconciliation_read_sources WHERE run_id=$1),
		(SELECT count(*) FROM reconciliation_detail_rows WHERE run_id=$1),
		(SELECT revision FROM reconciliation_read_runs WHERE id=$1),
		(SELECT count(*) FROM reconciliation_detail_events WHERE run_id=$1),
		(SELECT id FROM reconciliation_read_sources WHERE run_id=$2),
		(SELECT min(id) FROM reconciliation_detail_rows WHERE run_id=$2),
		(SELECT max(id) FROM reconciliation_detail_rows WHERE run_id=$2)`, failRun, nextRun).
		Scan(&sources, &rows, &version, &events, &sourceID, &firstRow, &lastRow); err != nil || sources != 0 || rows != 0 || version != 1 || events != 1 || sourceID != 4 || firstRow != 3*batch+1 || lastRow != 3*batch+2 {
		t.Fatalf("rollback allocation: sources=%d rows=%d version=%d events=%d next source=%d rows=%d–%d err=%v", sources, rows, version, events, sourceID, firstRow, lastRow, err)
	}
}
