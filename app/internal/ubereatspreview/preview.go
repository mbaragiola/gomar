// Package ubereatspreview projects bounded, reserved-ID synthetic Marketplace
// fixtures. It has no listener, network link, provider secrets, or live intake.
package ubereatspreview

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxFixture = 32 << 10

// Deliberately reserved by this test harness, not a real store/order allowlist.
var demoID = regexp.MustCompile(`^00000000-0000-4000-8000-00000000[0-9a-f]{4}$`)
var ErrFixture = errors.New("invalid or unsupported Uber Eats Marketplace demo fixture")

type Item struct {
	Name     string
	Quantity int64
}

type Event struct {
	StoreID, OrderID, EventID, Kind string
	Items                           []Item
}

type notice struct {
	Type string `json:"event_type"`
	ID   string `json:"event_id"`
	Meta struct {
		Store string `json:"user_id"`
		Order string `json:"resource_id"`
	} `json:"meta"`
}

func fixture(raw []byte, out any) bool {
	if len(raw) == 0 || len(raw) > maxFixture || !utf8.Valid(raw) || !json.Valid(raw) {
		return false
	}
	// Unknown provider fields, including URLs, must not become output.
	return json.NewDecoder(bytes.NewReader(raw)).Decode(out) == nil
}

func parseNotice(raw []byte, kind string) (notice, bool) {
	var n notice
	if !fixture(raw, &n) {
		return n, false
	}
	return n, n.Type == kind && demoID.MatchString(n.ID) &&
		demoID.MatchString(n.Meta.Store) && demoID.MatchString(n.Meta.Order)
}

// ParseAccepted requires both a new-order notification and matching details
// already marked ACCEPTED. It never acts on or fetches a resource_href.
func ParseAccepted(notification, details []byte) (Event, error) {
	n, ok := parseNotice(notification, "orders.notification")
	if !ok {
		return Event{}, ErrFixture
	}
	var order struct {
		ID    string `json:"id"`
		Brand string `json:"brand"`
		State string `json:"current_state"`
		Store struct {
			ID string `json:"id"`
		} `json:"store"`
		Cart struct {
			Items []struct {
				Title    string `json:"title"`
				Quantity int64  `json:"quantity"`
			} `json:"items"`
		} `json:"cart"`
	}
	if !fixture(details, &order) || order.State != "ACCEPTED" || order.Brand != "UBER_EATS" ||
		order.ID != n.Meta.Order || order.Store.ID != n.Meta.Store || len(order.Cart.Items) < 1 || len(order.Cart.Items) > 50 {
		return Event{}, ErrFixture
	}
	event := Event{StoreID: n.Meta.Store, OrderID: n.Meta.Order, EventID: n.ID, Kind: "accepted", Items: make([]Item, 0, len(order.Cart.Items))}
	for _, line := range order.Cart.Items {
		name := strings.TrimSpace(line.Title)
		if name == "" || utf8.RuneCountInString(name) > 120 || strings.IndexFunc(name, unicode.IsControl) >= 0 ||
			line.Quantity < 1 || line.Quantity > 999 {
			return Event{}, ErrFixture
		}
		event.Items = append(event.Items, Item{name, line.Quantity})
	}
	return event, nil
}

// ParseCancellation recognizes only the modern cancellation preview envelope.
func ParseCancellation(notification []byte) (Event, error) {
	n, ok := parseNotice(notification, "orders.cancel")
	if !ok {
		return Event{}, ErrFixture
	}
	return Event{StoreID: n.Meta.Store, OrderID: n.Meta.Order, EventID: n.ID, Kind: "cancelled", Items: []Item{}}, nil
}
