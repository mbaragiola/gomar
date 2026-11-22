// Package rappipreview maps synthetic Rappi restaurant events without
// opening a listener, using credentials, or calling the provider.
package rappipreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxBody = 32 << 10

var ErrFixture = errors.New("invalid or unsupported Rappi demo fixture")
var demoStore = regexp.MustCompile(`^demo-rappi-[a-z0-9][a-z0-9_-]{0,35}$`)
var numberID = regexp.MustCompile(`^[1-9][0-9]{0,23}$`)

type Route struct{ InternalStoreID, ExternalStoreID string }
type Item struct {
	Name     string
	Quantity int64
}
type Event struct {
	Provider, StoreID, OrderID, EventID, Kind string
	Items                                     []Item
}

func valid(route Route, body []byte) bool {
	return numberID.MatchString(route.InternalStoreID) && demoStore.MatchString(route.ExternalStoreID) && len(body) > 0 && len(body) <= maxBody && utf8.Valid(body)
}

func makeEvent(route Route, order, kind string) Event {
	// Rappi does not document a distinct event ID for either payload. This
	// key covers one received and one cancellation transition per demo order.
	h := sha256.Sum256([]byte("gomar/rappi-preview/v1\x00" + route.InternalStoreID + "\x00" + route.ExternalStoreID + "\x00" + order + "\x00" + kind))
	return Event{Provider: "rappi", StoreID: route.ExternalStoreID, OrderID: order, EventID: hex.EncodeToString(h[:]), Kind: kind}
}

// ParseNewOrder projects one NEW_ORDER webhook payload. This event carries
// the full order detail, but the preview keeps only names and quantities.
func ParseNewOrder(route Route, body []byte) (Event, error) {
	if !valid(route, body) {
		return Event{}, ErrFixture
	}
	var wire struct {
		Action json.RawMessage `json:"action"`
		Order  struct {
			ID    string `json:"order_id"`
			Items []struct {
				Name     string `json:"name"`
				Quantity int64  `json:"quantity"`
			} `json:"items"`
		} `json:"order_detail"`
		Store struct {
			InternalID string `json:"internal_id"`
			ExternalID string `json:"external_id"`
		} `json:"store"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.Action != nil || wire.Store.InternalID != route.InternalStoreID ||
		wire.Store.ExternalID != route.ExternalStoreID || !numberID.MatchString(wire.Order.ID) ||
		len(wire.Order.Items) < 1 || len(wire.Order.Items) > 50 {
		return Event{}, ErrFixture
	}
	event := makeEvent(route, wire.Order.ID, "received")
	event.Items = make([]Item, 0, len(wire.Order.Items))
	for _, item := range wire.Order.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" || len(name) > 240 || utf8.RuneCountInString(name) > 80 ||
			strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 ||
			item.Quantity < 1 || item.Quantity > 999 {
			return Event{}, ErrFixture
		}
		event.Items = append(event.Items, Item{Name: name, Quantity: item.Quantity})
	}
	return event, nil
}

// ParseCancellation projects one ORDER_EVENT_CANCEL. It binds the numeric
// Rappi store ID to the configured demo outlet. No fees are interpreted.
func ParseCancellation(route Route, body []byte) (Event, error) {
	if !valid(route, body) {
		return Event{}, ErrFixture
	}
	var wire struct {
		Event   string `json:"event"`
		OrderID string `json:"order_id"`
		StoreID string `json:"store_id"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.StoreID != route.InternalStoreID || !numberID.MatchString(wire.OrderID) {
		return Event{}, ErrFixture
	}
	switch wire.Event {
	case "cancel_by_user", "canceled_with_charge", "cancel_without_charges", "cancel_by_support", "cancel_by_support_with_charge",
		"cancel_by_application_user", "canceled_from_cms", "canceled_by_fraud_automation", "canceled_store_closed", "cancel_by_sk_with_charge":
		return makeEvent(route, wire.OrderID, "cancelled"), nil
	default:
		return Event{}, ErrFixture
	}
}
