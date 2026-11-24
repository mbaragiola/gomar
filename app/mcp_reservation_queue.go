package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

const mcpReservationQueuePath = mcpWorkspaceBase + "/reservation-requests"

func (a *app) registerMCPReservationQueue(add func(string, string, http.HandlerFunc)) {
	add(mcpReservationQueuePath, http.MethodGet, a.mcpRead(a.mcpListReservationRequests))
	add(mcpReservationQueuePath+"/{reservation}", http.MethodGet, a.mcpRead(a.mcpReadReservationRequest))
	add(mcpWorkspaceBase+"/reservation-availability", http.MethodGet, a.mcpRead(a.mcpReservationAvailability))
}

func mcpReservationQueueScope(r *http.Request, tx *sql.Tx, s mcpTokenScope) (int64, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return 0, err
	}
	if err := requireReservationStaff(r.Context(), tx, s.entityMembership, outlet); err != nil {
		return 0, err
	}
	return outlet, nil
}

func mcpReservationQueueQuery(r *http.Request, allowCursor bool) (time.Time, string, error) {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(params["date"]) != 1 {
		return time.Time{}, "", badInput
	}
	// Request IDs are receipt references, not credentials. The current public
	// routes do not accept a request ID for reading or changing a booking.
	for name, values := range params {
		if len(values) != 1 || name != "date" && (!allowCursor || name != "after_id") {
			return time.Time{}, "", badInput
		}
	}
	date, err := reservationDay(params.Get("date"))
	if err != nil {
		return time.Time{}, "", err
	}
	after := params.Get("after_id")
	if _, present := params["after_id"]; present && !reservationID.MatchString(after) {
		return time.Time{}, "", badInput
	}
	return date, after, nil
}

const mcpReservationQueueColumns = `SELECT id,status,party_size,start_at FROM reservation_requests`
const mcpReservationQueueWindow = ` WHERE outlet_id=$1 AND start_at >= $2 AND start_at < $3`

func mcpScanReservationRequest(row interface{ Scan(...any) error }) (mcpclient.ReservationRequest, error) {
	var entry mcpclient.ReservationRequest
	if err := row.Scan(&entry.ID, &entry.Status, &entry.PartySize, &entry.StartAt); err != nil {
		return entry, err
	}
	stamp, err := time.Parse(time.RFC3339Nano, entry.StartAt)
	if err != nil || !reservationID.MatchString(entry.ID) {
		return entry, errors.New("invalid stored reservation record")
	}
	entry.StartAt = stamp.In(reservationLocation).Format(time.RFC3339Nano)
	return entry, nil
}

func (a *app) mcpListReservationRequests(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := a.mcpReservationReadOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	day, after, err := mcpReservationQueueQuery(r, true)
	if err != nil {
		return nil, err
	}
	from, to := day.UTC().Format(utcSecondPrefix), day.AddDate(0, 0, 1).UTC().Format(utcSecondPrefix)
	var cursorStart string
	if after != "" {
		err := tx.QueryRowContext(r.Context(), `SELECT start_at FROM reservation_requests`+mcpReservationQueueWindow+` AND id=$4`,
			outlet, from, to, after).Scan(&cursorStart)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, badInput
		}
		if err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(r.Context(), mcpReservationQueueColumns+mcpReservationQueueWindow+
		` AND ($4='' OR (start_at,id)>($4,$5)) ORDER BY start_at,id LIMIT $6`, outlet, from, to, cursorStart, after, mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := mcpclient.ReservationRequests{Date: day.Format(time.DateOnly), Timezone: reservationTimezone,
		Requests: []mcpclient.ReservationRequest{}}
	for rows.Next() {
		item, err := mcpScanReservationRequest(rows)
		if err != nil {
			return nil, err
		}
		result.Requests = append(result.Requests, item)
	}
	if len(result.Requests) > mcpPageSize {
		result.Requests = result.Requests[:mcpPageSize]
		result.NextID = result.Requests[mcpPageSize-1].ID
	}
	return result, rows.Err()
}

func (a *app) mcpReadReservationRequest(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := a.mcpReservationReadOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("reservation")
	if !reservationID.MatchString(id) {
		return nil, reservationMissing
	}
	day, _, err := mcpReservationQueueQuery(r, false)
	if err != nil {
		return nil, err
	}
	from, to := day.UTC().Format(utcSecondPrefix), day.AddDate(0, 0, 1).UTC().Format(utcSecondPrefix)
	entry, err := mcpScanReservationRequest(tx.QueryRowContext(r.Context(), mcpReservationQueueColumns+mcpReservationQueueWindow+
		` AND id=$4`, outlet, from, to, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, reservationMissing
	}
	return entry, err
}
