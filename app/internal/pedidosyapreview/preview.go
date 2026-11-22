// Package pedidosyapreview maps synthetic PedidosYa restaurant payloads.
// It has no listener, authentication, provider client, or production intake.
package pedidosyapreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxBody = 32 << 10
const maxProducts = 50

var ErrFixture = errors.New("invalid or unsupported PedidosYa demo fixture")
var demoID = regexp.MustCompile(`^demo-[a-z0-9][a-z0-9_-]{0,47}$`)
var quantity = regexp.MustCompile(`^[1-9][0-9]{0,2}$`)

type Item struct {
	Name     string
	Quantity int64
}
type Event struct {
	Provider, StoreID, OrderID, EventID, Kind string
	Items                                     []Item
}

func envelope(store, order, kind string, body []byte) (Event, error) {
	if !demoID.MatchString(store) || !demoID.MatchString(order) || len(body) == 0 || len(body) > maxBody || !utf8.Valid(body) {
		return Event{}, ErrFixture
	}
	// A stable per-kind demo key. The provider does not document an event ID for
	// these callbacks. This is not a live deduplication contract.
	key := sha256.Sum256([]byte("gomar/pedidosya-demo/v1\x00" + store + "\x00" + order + "\x00" + kind))
	return Event{Provider: "pedidosya", StoreID: store, OrderID: order, EventID: hex.EncodeToString(key[:]), Kind: kind}, nil
}

// ParseDispatch accepts a synthetic, already accepted indirect order fixture.
// Its local order ID stands in for the plugin ID later referenced by status updates.
func ParseDispatch(store, order string, body []byte) (Event, error) {
	event, err := envelope(store, order, "accepted", body)
	if err != nil {
		return Event{}, err
	}
	var wire struct {
		Token    string `json:"token"`
		Code     string `json:"code"`
		Test     *bool  `json:"test"`
		Products []struct {
			Name     string `json:"name"`
			Quantity string `json:"quantity"`
		} `json:"products"`
	}
	// Additional provider properties are intentionally ignored, not persisted.
	if json.Unmarshal(body, &wire) != nil || !demoID.MatchString(wire.Token) || !demoID.MatchString(wire.Code) ||
		wire.Test == nil || *wire.Test || len(wire.Products) == 0 || len(wire.Products) > maxProducts {
		return Event{}, ErrFixture
	}
	event.Items = make([]Item, 0, len(wire.Products))
	for _, product := range wire.Products {
		name := strings.TrimSpace(product.Name)
		if name == "" || len(name) > 240 || utf8.RuneCountInString(name) > 80 || strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) != -1 || !quantity.MatchString(product.Quantity) {
			return Event{}, ErrFixture
		}
		n, err := strconv.ParseInt(product.Quantity, 10, 64)
		if err != nil || n > 99 {
			return Event{}, ErrFixture
		}
		event.Items = append(event.Items, Item{Name: name, Quantity: n})
	}
	return event, nil
}

// ParseCancellation models the documented plugin status payload only. Whether
// PedidosYa delivers it for a particular indirect integration is unverified.
func ParseCancellation(store, order string, body []byte) (Event, error) {
	event, err := envelope(store, order, "cancelled", body)
	if err != nil {
		return Event{}, err
	}
	var wire struct {
		Status  string  `json:"status"`
		Message *string `json:"message"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.Status != "ORDER_CANCELLED" || wire.Message == nil {
		return Event{}, ErrFixture
	}
	return event, nil
}
