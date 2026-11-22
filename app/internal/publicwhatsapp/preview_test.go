package publicwhatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mbaragiola/gomar/app/internal/publicconcierge"
)

func sample(text string) []byte {
	quoted, _ := json.Marshal(text)
	return []byte(`{"object":"whatsapp_business_account","entry":[{"id":"101","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"202"},"messages":[{"from":"9990000000","id":"wamid.fixture==","type":"text","text":{"body":` + string(quoted) + `}}]}}]}]}`)
}
func TestBoundMetaDraftAndExplicitIntents(t *testing.T) {
	route := Route{"101", "202", "fixture-site"}
	for _, tc := range []struct {
		body         string
		kind         publicconcierge.Kind
		date, start  string
		n            int
		confirmation bool
	}{
		{"menú", publicconcierge.Menu, "", "", 0, false},
		{"¿Cómo cancelo?", publicconcierge.CancellationPolicy, "", "", 0, false},
		{"disponibilidad 2030-04-08 4", publicconcierge.Availability, "2030-04-08", "", 4, false},
		{"confirmo reserva 2030-04-08 21:00 4 | Ana Pérez", publicconcierge.BookingRequest, "", "2030-04-08T21:00:00-03:00", 4, true},
		{"quiero reserva lunes", publicconcierge.Help, "", "", 0, false},
		{"confirmo reserva 2030-02-30 21:00 4 | Ana Pérez", publicconcierge.Help, "", "", 0, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			d, err := Replay(t.Context(), sample(tc.body), route, func(ctx context.Context, i publicconcierge.Intent, s publicconcierge.Sink) error {
				if i.Kind != tc.kind || i.Date != tc.date || i.StartAt != tc.start || i.PartySize != tc.n || i.Venue != route.Venue || i.ConfirmedByGuest != tc.confirmation || len(i.EventID) != 64 || strings.Contains(i.EventID, "wamid") {
					t.Fatal(i)
				}
				if i.Kind == publicconcierge.BookingRequest && i.GuestName != "Ana Pérez" {
					t.Fatal(i.GuestName)
				}
				r := publicconcierge.Reply{Text: "Acá tenés la información."}
				if i.Kind == publicconcierge.Menu {
					r.Document = &publicconcierge.Document{URL: "https://restaurant.example/api/v1/public/reservas/fixture-site/menu.pdf", Filename: "menu.pdf", ContentType: "application/pdf"}
				}
				return s.Publish(ctx, r)
			})
			if err != nil || d.To != "9990000000" || d.MessagingProduct != "whatsapp" {
				t.Fatal(d, err)
			}
			if tc.kind == publicconcierge.Menu && d.Document == nil {
				t.Fatal(d)
			}
			if tc.kind == publicconcierge.Help && (d.Text == nil || !strings.Contains(d.Text.Body, "confirmo reserva")) {
				t.Fatal(d)
			}
			raw, err := d.JSON()
			if err != nil || !json.Valid(raw) {
				t.Fatal(string(raw), err)
			}
		})
	}
}
func TestNoUnboundOrAmbiguousInput(t *testing.T) {
	route := Route{"101", "202", "fixture-site"}
	wrongBusiness := Route{"303", "202", "fixture-site"}
	wrongNumber := Route{"101", "303", "fixture-site"}
	for _, item := range []struct {
		route   Route
		payload []byte
	}{
		{wrongBusiness, sample("menu")}, {wrongNumber, sample("menu")},
		{route, []byte(`{"object":"whatsapp_business_account","entry":[]}`)},
		{route, []byte(strings.Replace(string(sample("menu")), `"type":"text"`, `"type":"document"`, 1))},
		{route, []byte(strings.Replace(string(sample("menu")), `"from":"9990000000"`, `"from":"sender-from-url"`, 1))},
		{route, sample(strings.Repeat("x", 321))}, {route, []byte(strings.Repeat(" ", 1<<15))},
	} {
		called := false
		d, err := Replay(t.Context(), item.payload, item.route, func(context.Context, publicconcierge.Intent, publicconcierge.Sink) error { called = true; return nil })
		if !errors.Is(err, ErrIgnored) || called || d.To != "" {
			t.Fatal(d, err, called)
		}
	}
}
