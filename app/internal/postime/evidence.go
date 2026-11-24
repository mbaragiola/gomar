// Package postime describes clock evidence on sales queued by Caja.
// A device clock is never proof of when a sale or payment happened.
package postime

import (
	"fmt"
	"regexp"
	"time"
)

const (
	NotReported = "not_reported"
	Unverified  = "unverified"
	Review      = "review"

	// A broad heuristic only. A sale may legitimately spend longer in the queue;
	// neither deviation rejects the sale or changes accounting.
	maxClockAhead = 5 * time.Minute
	maxQueueDelay = 24 * time.Hour
)

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var utcPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)

func ValidRequestID(s string) bool { return requestIDPattern.MatchString(s) }

// ParseUTC only accepts an explicit UTC string, not an offset or a local time.
// Do not normalize the stored text: that is the exact claim accepted at insert.
func ParseUTC(raw string) (time.Time, bool) {
	if !utcPattern.MatchString(raw) {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	return t, err == nil && t.Year() > 0
}

func Status(reported string, registered time.Time) (string, bool) {
	if reported == "" {
		return NotReported, true
	}
	t, ok := ParseUTC(reported)
	if !ok {
		return "", false
	}
	if t.After(registered.Add(maxClockAhead)) || t.Before(registered.Add(-maxQueueDelay)) {
		return Review, true
	}
	return Unverified, true
}

func Handoff(entity, outlet int64, request string) string {
	return fmt.Sprintf("/pos#entity=%d&outlet=%d&venta=%s", entity, outlet, request)
}

// Evidence contains only the references and time evidence allowed for this read.
type Evidence struct {
	RequestID      string `json:"request_id"`
	RegisteredAt   string `json:"registered_at_utc"`
	TillReportedAt string `json:"till_reported_at_utc,omitempty"`
	TillTimeStatus string `json:"till_time_status"`
	HandoffURL     string `json:"handoff_url"`
}
