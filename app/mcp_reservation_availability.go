package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func (a *app) mcpReservationAvailability(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpReservationQueueScope(r, tx, s)
	if err != nil {
		return nil, err
	}
	if !a.publicReservations {
		return nil, reservationUnavailable
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["date"]) != 1 || len(q["party_size"]) != 1 {
		return nil, badInput
	}
	party, err := strconv.Atoi(q.Get("party_size"))
	if err != nil || party < 1 || party > 12 || strconv.Itoa(party) != q.Get("party_size") {
		return nil, badInput
	}
	day, err := reservationDay(q.Get("date"))
	now := a.tokenTime()
	if err != nil || !reservationBookableDay(day, now) {
		return nil, badInput
	}
	var enabled bool
	err = tx.QueryRowContext(r.Context(), `SELECT enabled FROM reservation_venues WHERE outlet_id=$1`, outlet).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, reservationUnavailable
	}
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, reservationUnavailable
	}
	slots, err := reservationAvailabilityInTx(r.Context(), tx, outlet, day, party, now)
	if err != nil {
		return nil, err
	}
	result := mcpclient.ReservationAvailability{Date: q.Get("date"), Timezone: reservationTimezone, PartySize: party, Slots: []mcpclient.ReservationAvailableStart{}}
	for _, slot := range slots {
		at, err := time.Parse(time.RFC3339, slot.StartAt)
		canonical, valid := reservationStart(slot.StartAt, now)
		if err != nil || valid != nil || canonical != slot.StartAt || at.In(reservationLocation).Format(time.DateOnly) != result.Date {
			return nil, errors.New("invalid stored reservation slot")
		}
		result.Slots = append(result.Slots, mcpclient.ReservationAvailableStart{StartAt: at.In(reservationLocation).Format(time.RFC3339)})
	}
	return result, nil
}
