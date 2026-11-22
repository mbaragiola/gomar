package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgresReservationsConcurrentStaffConfirmationAndMenu(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const id = 724001
	const password = "synthetic reservation test password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(724001,'reservation-test-pg',$1,true,false,false)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO entities(id,name,cuit,active) VALUES(724001,'Synthetic reservation entity','00000724001',true)`,
		`INSERT INTO outlets(id,entity_id,name,active) VALUES(724001,724001,'Synthetic reservation outlet',true)`,
		`INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(724001,724001,724001,'administration',true)`,
		`INSERT INTO outlet_grants(membership_id,outlet_id) VALUES(724001,724001)`,
		`INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled)
     VALUES(724001,'reserva-qa-pg','Synthetic reservation outlet','Avisá si no venís.',true)`,
		`INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(724001,'Mesa 1',4)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	local := now.In(reservationLocation).AddDate(0, 0, 1)
	start := time.Date(local.Year(), local.Month(), local.Day(), 21, 0, 0, 0, reservationLocation)
	if _, err := db.ExecContext(ctx, `INSERT INTO reservation_slots(outlet_id,start_at) VALUES($1,$2)`, id, start.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	svc := reservationsService{db: db}
	slots, err := svc.availability(ctx, "reserva-qa-pg", start.Format(time.DateOnly), 4, now)
	if err != nil || len(slots) != 1 || slots[0].StartAt != start.UTC().Format(time.RFC3339) {
		t.Fatal(slots, err)
	}
	ids := []string{}
	for _, guest := range []string{"Persona Uno", "Persona Dos"} {
		item, err := svc.request(ctx, "reserva-qa-pg", reservationInput{GuestName: guest, PartySize: 4, StartAt: start.Format(time.RFC3339)}, now)
		if err != nil || item.Status != "pending" || item.CancellationPolicy == nil || *item.CancellationPolicy != "Avisá si no venís." {
			t.Fatal(item, err)
		}
		ids = append(ids, item.ID)
	}
	a := &app{db: db, host: "example.com", publicReservations: true}
	handler := a.routes()
	cookies := postgresTestCookies(t, handler, "reservation-test-pg", password)
	invoke := func(method, path, body, kind string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if kind != "" {
			r.Header.Set("Content-Type", kind)
		}
		if authenticated {
			for _, cookie := range cookies {
				r.AddCookie(cookie)
				if cookie.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", cookie.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	const panels = "/api/v1/entities/724001/outlets/724001/reservations"
	const pdf = "%PDF-1.6\n%%synthetic\n%%EOF"
	if w := invoke("PUT", panels+"/menu", pdf, "application/pdf", true); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if w := invoke("GET", "/api/v1/public/reservas/reserva-qa-pg/menu.pdf", "", "", false); w.Code != http.StatusOK || w.Body.String() != pdf {
		t.Fatal(w.Code, w.Body)
	}
	startTogether := make(chan struct{})
	result := make(chan *httptest.ResponseRecorder, len(ids))
	var group sync.WaitGroup
	for _, requestID := range ids {
		group.Add(1)
		go func() {
			defer group.Done()
			<-startTogether
			result <- invoke("POST", panels+"/"+requestID+"/confirm", "", "", true)
		}()
	}
	close(startTogether)
	group.Wait()
	close(result)
	counts := map[int]int{}
	for w := range result {
		counts[w.Code]++
		if w.Code != 200 && w.Code != 409 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	slots, err = svc.availability(ctx, "reserva-qa-pg", start.Format(time.DateOnly), 4, now)
	if err != nil || len(slots) != 0 {
		t.Fatal(slots, err)
	}
	var accepted string
	if err := db.QueryRowContext(ctx, `SELECT id FROM reservation_requests WHERE outlet_id=$1 AND status='confirmed'`, id).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if w := invoke("POST", panels+"/"+accepted+"/cancel", "", "", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	slots, err = svc.availability(ctx, "reserva-qa-pg", start.Format(time.DateOnly), 4, now)
	if err != nil || len(slots) != 1 {
		t.Fatal(slots, err)
	}
	var wrong sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT table_label FROM reservation_requests WHERE id=$1`, accepted).Scan(&wrong); err != nil || wrong.Valid {
		t.Fatal(wrong, err)
	}
}
