package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresMCPReservationConfirmWorkflow(t *testing.T) {
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
		`INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(724001,'Mesa 1',4),(724001,'Mesa 2',6)`,
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
	a := &app{db: db, host: "example.com", publicReservations: true, enableMCPReservationConfirmProposals: true}
	handler := a.routes()
	cookies := postgresTestCookies(t, handler, "reservation-test-pg", password)
	otherLogin := postgresTestCookies(t, handler, "reservation-test-pg", password)
	call := func(method, path, body, token string, login []*http.Cookie, expected int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if login != nil {
			r.Header.Set("Origin", "http://example.com")
			for _, c := range login {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, expected, w.Body)
		}
		return w
	}
	const browser = "/api/v1/entities/724001/outlets/724001/reservation-confirm-proposals"
	const api = "/api/v1/mcp/entities/724001/outlets/724001/reservation-confirm-proposals"
	var session struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	issued := call("POST", proposalSessionsAuthPath, `{"entity_id":724001,"outlet_id":724001,"actions":["reservation_confirm"]}`, "", cookies, 201)
	if err := json.Unmarshal(issued.Body.Bytes(), &session); err != nil || len(session.Token) != 43 {
		t.Fatal("missing synthetic session")
	}
	queue := func(index int) (string, string, string) {
		t.Helper()
		body := fmt.Sprintf(`{"request_id":"55555555-0000-4000-8000-00000000000%d","confirmation":{"reservation_reference":%q}}`, index+1, ids[index])
		first := call("POST", api, body, session.Token, nil, 201)
		retry := call("POST", api, body, session.Token, nil, 200)
		if first.Body.String() != retry.Body.String() || strings.Contains(first.Body.String(), "reservation_reference") {
			t.Fatal("unsafe bearer receipt")
		}
		var receipt reservationConfirmWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID < 1 || receipt.Status != "pending" {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		result := call("GET", path, "", "", cookies, 200)
		var preview reservationConfirmWireDetails
		if err := json.Unmarshal(result.Body.Bytes(), &preview); err != nil || preview.Confirmation == nil ||
			preview.Confirmation.Reference != ids[index] || preview.Confirmation.TableLabel != "Mesa 1" || preview.Confirmation.TableCapacity != 4 ||
			preview.Confirmation.PartySize != 4 || preview.Confirmation.StartLocal != start.Format(time.RFC3339) || len(preview.Digest) != 64 ||
			strings.Contains(result.Body.String(), "Persona") || strings.Contains(result.Body.String(), "Avisá") {
			t.Fatal(preview, err)
		}
		return path, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, preview.Digest), body
	}
	check := func(expected int) {
		t.Helper()
		var confirmed, otherTable, policy int
		err := db.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE status='confirmed'),
 count(*) FILTER(WHERE table_label='Mesa 2'),
 (SELECT count(*) FROM reservation_request_policies WHERE policy='Avisá si no venís.')
 FROM reservation_requests WHERE outlet_id=$1`, id).Scan(&confirmed, &otherTable, &policy)
		if err != nil || confirmed != expected || otherTable != 0 || policy != 2 {
			t.Fatal(confirmed, otherTable, policy, err)
		}
	}
	p1, d1, body := queue(0)
	p2, d2, _ := queue(1)
	check(0)
	call("POST", p1+"/decision", d1, "", otherLogin, 404)
	call("POST", p1+"/decision", strings.Replace(d1, "true", "false", 1), "", cookies, 422)
	outcome := call("POST", p1+"/decision", d1, "", cookies, 200)
	var approved reservationConfirmWireDetails
	if err := json.Unmarshal(outcome.Body.Bytes(), &approved); err != nil || approved.Status != "approved" || approved.ReservationReference == nil || *approved.ReservationReference != ids[0] || approved.Confirmation != nil {
		t.Fatal(approved, err)
	}
	call("POST", p1+"/decision", d1, "", cookies, 409)
	call("POST", p2+"/decision", d2, "", cookies, 409)
	if got := call("POST", api, body, session.Token, nil, 200).Body.String(); strings.Contains(got, "reservation_reference") {
		t.Fatal("completed bearer receipt disclosed reservation reference")
	}
	call("DELETE", fmt.Sprintf("%s/%d", proposalSessionsAuthPath, session.ID), "", "", cookies, 204)
	if got := call("GET", p2, "", "", cookies, 200).Body.String(); !strings.Contains(got, `"revoked"`) || strings.Contains(got, `"confirmation"`) {
		t.Fatal("revoked preview was not cleared")
	}
	check(1)
}
