package mcpclient

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"time"
)

var reservationReference = regexp.MustCompile("^[0-9a-f]{32}$")

type ReservationRequest struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	PartySize int    `json:"party_size"`
	StartAt   string `json:"start_at"`
}
type ReservationRequests struct {
	Date     string               `json:"date"`
	Timezone string               `json:"timezone"`
	Requests []ReservationRequest `json:"requests"`
	NextID   string               `json:"next_id,omitempty"`
}

func validQueueEntry(e ReservationRequest, date string, location *time.Location) bool {
	if !reservationReference.MatchString(e.ID) || e.PartySize < 1 || e.PartySize > 12 {
		return false
	}
	switch e.Status {
	case "pending", "confirmed", "cancelled", "expired":
	default:
		return false
	}
	stamp, err := time.Parse(time.RFC3339Nano, e.StartAt)
	return err == nil && stamp.In(location).Format(time.DateOnly) == date
}

func (c *Client) ListReservationRequests(ctx context.Context, entity, outlet int64, date, after string) (ReservationRequests, error) {
	var result ReservationRequests
	if _, err := parseReportDate(date); err != nil {
		return result, err
	}
	q := url.Values{"date": {date}}
	if after != "" {
		if !reservationReference.MatchString(after) {
			return result, errors.New("usá el next_id de la página anterior")
		}
		q.Set("after_id", after)
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/reservation-requests", q, &result); err != nil {
		return ReservationRequests{}, err
	}
	if result.Date != date || result.Timezone != buenosAires || result.Requests == nil || len(result.Requests) > workspacePageSize {
		return ReservationRequests{}, invalidResponse
	}
	local, err := time.LoadLocation(buenosAires)
	if err != nil {
		return ReservationRequests{}, invalidResponse
	}
	seen := map[string]bool{after: true}
	for _, entry := range result.Requests {
		if seen[entry.ID] || !validQueueEntry(entry, date, local) {
			return ReservationRequests{}, invalidResponse
		}
		seen[entry.ID] = true
	}
	if result.NextID != "" && (len(result.Requests) != workspacePageSize || result.NextID != result.Requests[len(result.Requests)-1].ID) {
		return ReservationRequests{}, invalidResponse
	}
	return result, nil
}

func (c *Client) ReadReservationRequest(ctx context.Context, entity, outlet int64, date, id string) (ReservationRequest, error) {
	var result ReservationRequest
	if _, err := parseReportDate(date); err != nil {
		return result, err
	}
	if !reservationReference.MatchString(id) {
		return result, errors.New("usá un reservation_id devuelto por gomar_list_reservation_requests")
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/reservation-requests/"+id, url.Values{"date": {date}}, &result); err != nil {
		return ReservationRequest{}, err
	}
	local, err := time.LoadLocation(buenosAires)
	if err != nil || result.ID != id || !validQueueEntry(result, date, local) {
		return ReservationRequest{}, invalidResponse
	}
	return result, nil
}
