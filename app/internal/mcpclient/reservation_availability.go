package mcpclient

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
)

type ReservationAvailableStart struct {
	StartAt string `json:"start_at"`
}

type ReservationAvailability struct {
	Date      string                      `json:"date"`
	Timezone  string                      `json:"timezone"`
	PartySize int                         `json:"party_size"`
	Slots     []ReservationAvailableStart `json:"slots"`
}

func (c *Client) ReservationAvailability(ctx context.Context, entity, outlet int64, date string, partySize int) (ReservationAvailability, error) {
	var out ReservationAvailability
	if _, err := parseReportDate(date); err != nil {
		return out, err
	}
	if partySize < 1 || partySize > 12 {
		return out, errors.New("indicá una cantidad de personas de 1 a 12")
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/reservation-availability", url.Values{"date": {date}, "party_size": {strconv.Itoa(partySize)}}, &out); err != nil {
		return ReservationAvailability{}, err
	}
	local, err := time.LoadLocation(buenosAires)
	if err != nil || out.Date != date || out.Timezone != buenosAires || out.PartySize != partySize || out.Slots == nil || len(out.Slots) > 16 {
		return ReservationAvailability{}, invalidResponse
	}
	var previous time.Time
	for _, slot := range out.Slots {
		at, err := time.Parse(time.RFC3339, slot.StartAt)
		ba := at.In(local)
		if err != nil || ba.Format(time.RFC3339) != slot.StartAt || ba.Format(time.DateOnly) != date ||
			ba.Second() != 0 || ba.Nanosecond() != 0 || (ba.Hour()*60+ba.Minute())%90 != 0 || !at.After(previous) {
			return ReservationAvailability{}, invalidResponse
		}
		previous = at
	}
	return out, nil
}
