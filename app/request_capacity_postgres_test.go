package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type heldFixtureBody struct {
	io.ReadCloser
	started chan struct{}
	release chan struct{}
}

func (b *heldFixtureBody) Read(p []byte) (int, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-b.release
	return b.ReadCloser.Read(p)
}

func TestPostgresReservationAndUploadCapacity(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'fixture-capacity','!synthetic',TRUE,FALSE,FALSE);
		INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000001',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Fixture A',TRUE),(2,1,'Fixture B',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2);
		INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Fixture supplier','00000000003',TRUE);
		INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,
		 issue_date,currency,total_minor,description,status,revision,created_at,updated_at)
		VALUES(1,1,1,1,'00000000003','invoice','A',1,1,'2026-11-01','ARS',100,'','draft',1,'before','after');
		INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled) VALUES(1,'fixture-venue','Fixture A','',TRUE);
		INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(1,'Mesa 1',4);
		INSERT INTO reservation_slots(outlet_id,start_at)
		SELECT 1,to_char(TIMESTAMP '2001-01-01 12:00' + i * INTERVAL '90 minutes','YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM generate_series(0,6) i;
		INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,table_label,created_at)
		SELECT lpad(to_hex(i+1),32,'0'),1,to_char(TIMESTAMP '2001-01-01 12:00' + (i / 19) * INTERVAL '90 minutes','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
			'Fictional guest',2,CASE WHEN i=0 THEN 'confirmed' WHEN i=1 THEN 'cancelled' ELSE 'pending' END,
			CASE WHEN i=0 THEN 'Mesa 1' ELSE NULL END,'2001-01-01T10:00:00Z'
		FROM generate_series(0,131) i;`)
	local := time.Now().In(reservationLocation).AddDate(0, 0, 1)
	start := time.Date(local.Year(), local.Month(), local.Day(), 21, 0, 0, 0, reservationLocation)
	seed(`INSERT INTO reservation_slots(outlet_id,start_at) VALUES(1,$1)`, start.UTC().Format(time.RFC3339))
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &app{db: db, host: "example.com", privateRoot: root, publicReservations: true, enableMCP: true}
	router := a.routes()
	cookies := nativeDocumentSession(t, a, router, 1)
	serve := func(r *http.Request, authenticated bool) *httptest.ResponseRecorder {
		if authenticated {
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r.WithContext(ctx))
		return w
	}
	check := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("got %d, want %d: %s", w.Code, want, w.Body.String())
		}
	}
	const guestPath = "http://example.com/api/v1/public/reservas/fixture-venue/requests"
	const intakePath = "http://example.com/api/v1/entities/1/outlets/2/intake"
	const attachmentPath = "http://example.com/api/v1/entities/1/documents/1/attachments"
	guest := func() *http.Request {
		body := fmt.Sprintf(`{"guest_name":"Fictional guest","party_size":2,"start_at":%q}`, start.Format(time.RFC3339))
		r := httptest.NewRequest("POST", guestPath, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	png := nativeDocumentPNG(t)
	hold := func(r *http.Request, authenticated bool) (<-chan *httptest.ResponseRecorder, func()) {
		t.Helper()
		gate := &heldFixtureBody{ReadCloser: r.Body, started: make(chan struct{}), release: make(chan struct{})}
		r.Body = gate
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- serve(r, authenticated) }()
		release := func() {
			select {
			case <-gate.release:
			default:
				close(gate.release)
			}
		}
		t.Cleanup(release)
		select {
		case <-gate.started:
		case w := <-result:
			t.Fatalf("request ended before reading the fixture: %d %s", w.Code, w.Body.String())
		case <-time.After(2 * time.Second):
			t.Fatal("request did not reach fixture read")
		}
		return result, release
	}
	receive := func(ch <-chan *httptest.ResponseRecorder, status int) {
		t.Helper()
		select {
		case w := <-ch:
			check(w, status)
		case <-time.After(2 * time.Second):
			t.Fatal("ordinary fixture request did not finish")
		}
	}
	first, finishFirst := hold(guest(), false)
	second, finishSecond := hold(guest(), false)
	image, finishImage := hold(nativeDocumentUpload(t, intakePath, "fixture.png", png), true)
	busy := []*httptest.ResponseRecorder{
		serve(guest(), false),
		serve(nativeDocumentUpload(t, attachmentPath, "fixture.png", png), true),
	}
	for _, w := range busy {
		check(w, 503)
		if w.Header().Get("Retry-After") != "2" {
			t.Fatal("capacity rejection lacked retry hint")
		}
	}
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if err := lockPostgresOutlet(ctx, blocker, 1); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err := blocker.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	finishFirst()
	finishSecond()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture reservation did not reach the database")
		}
		time.Sleep(5 * time.Millisecond)
	}
	finishImage()
	staff := make(chan *httptest.ResponseRecorder, 1)
	go func() { staff <- serve(httptest.NewRequest("GET", "http://example.com/api/v1/entities", nil), true) }()
	receive(staff, 200)
	receive(image, 201)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	receive(first, 201)
	receive(second, 201)
	check(serve(nativeDocumentUpload(t, attachmentPath, "fixture.png", png), true), 201)

	if err := a.expirePendingReservations(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var expired int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reservation_requests WHERE status='expired'`).Scan(&expired); err != nil || expired != reservationExpiryBatch {
		t.Fatalf("expiry exceeded batch or missed candidates: %d %v", expired, err)
	}
	if err := a.expirePendingReservations(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	keyRequest := httptest.NewRequest("POST", "http://example.com/api/v1/auth/mcp-token", strings.NewReader(`{"entity_id":1,"outlet_id":1}`))
	keyRequest.Header.Set("Content-Type", "application/json")
	keyResponse := serve(keyRequest, true)
	check(keyResponse, 201)
	var key struct{ Token string }
	if err := json.Unmarshal(keyResponse.Body.Bytes(), &key); err != nil || key.Token == "" {
		t.Fatal("no synthetic MCP read key", err)
	}
	mcp := func(path string, status int) string {
		t.Helper()
		req := httptest.NewRequest("GET", "http://example.com/api/v1/mcp"+path, nil)
		req.Header.Set("Authorization", "Bearer "+key.Token)
		result := serve(req, false)
		check(result, status)
		return result.Body.String()
	}
	base := "/entities/1/outlets/1/reservation"
	status := mcp(base+"-status?date=2001-01-01", 200)
	if !strings.Contains(status, `"expired":{"status":"expired","request_count":130,"party_size_total":260}`) ||
		!strings.Contains(status, `"confirmed","request_count":1`) || !strings.Contains(status, `"cancelled","request_count":1`) {
		t.Fatalf("unexpected terminal/request counts: %s", status)
	}
	queue := mcp(base+"-requests?date=2001-01-01", 200)
	if !strings.Contains(queue, `"status":"expired"`) || strings.Contains(queue, "Fictional guest") {
		t.Fatalf("scoped MCP queue did not redact the guest or reflect expiry: %s", queue)
	}
	mcp("/entities/1/outlets/2/reservation-requests?date=2001-01-01", 404)
	nativePath := "http://example.com/api/v1/entities/1/outlets/1/reservations"
	nativeQueue := serve(httptest.NewRequest("GET", nativePath+"?date=2001-01-01", nil), true)
	check(nativeQueue, 200)
	if !strings.Contains(nativeQueue.Body.String(), `"status":"expired"`) {
		t.Fatal("native queue did not show expired records")
	}
	check(serve(httptest.NewRequest("POST", nativePath+"/00000000000000000000000000000003/confirm", nil), true), 409)
}
