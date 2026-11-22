package publicconcierge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	info     VenueInfo
	err      error
	slots    []time.Time
	requests int
	pending  map[string]Request
}

func (f *fixture) Info(_ context.Context, v string) (VenueInfo, error) {
	if v != "local-demo" {
		return VenueInfo{}, ErrClosed
	}
	return f.info, f.err
}
func (f *fixture) Available(_ context.Context, _ string, _ string, _ int) ([]time.Time, error) {
	return f.slots, nil
}
func (f *fixture) Request(_ context.Context, _ string, r Request) (Pending, error) {
	f.requests++
	if prev, ok := f.pending[r.EventID]; ok && prev != r {
		return Pending{}, ErrUnavailable
	}
	f.pending[r.EventID] = r
	return Pending{Reference: "ref-demo"}, nil
}

type fakeSink struct {
	posted []Reply
	err    error
}

func (s *fakeSink) Publish(_ context.Context, r Reply) error {
	s.posted = append(s.posted, r)
	return s.err
}
func setup() (*fixture, *fakeSink, Concierge) {
	f := &fixture{pending: map[string]Request{}}
	s := &fakeSink{}
	return f, s, Concierge{Catalog: f, Sink: s, PublicOrigin: "https://restaurant.example"}
}
func send(t *testing.T, c Concierge, s *fakeSink, in Intent) Reply {
	t.Helper()
	in.Venue = "local-demo"
	if err := c.Handle(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	return s.posted[len(s.posted)-1]
}
func TestStructuredRequestAndDuplicateEvent(t *testing.T) {
	f, s, c := setup()
	in := Intent{Kind: BookingRequest, StartAt: "2030-04-08T20:45:00-03:00", PartySize: 4, GuestName: "Ana Pérez", EventID: "event-demo-17"}
	if r := send(t, c, s, in); !strings.Contains(r.Text, "confirmá") || f.requests != 0 {
		t.Fatal(r, f.requests)
	}
	in.ConfirmedByGuest = true
	for range 2 {
		if r := send(t, c, s, in); r.Text != "Recibimos tu solicitud. La mesa todavía no está confirmada." || strings.Contains(r.Text, "Ana") {
			t.Fatal(r)
		}
	}
	if len(f.pending) != 1 || f.pending[in.EventID].GuestName != in.GuestName {
		t.Fatal("expected one request from fake idempotent catalog")
	}
	in.GuestName = "Otra Persona"
	if r := send(t, c, s, in); !strings.Contains(r.Text, "no está disponible") {
		t.Fatal(r)
	}
}
func TestAvailabilityInArgentinaAndBounded(t *testing.T) {
	f, s, c := setup()
	origin, err := time.Parse(time.RFC3339, "2030-04-09T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	f.slots = []time.Time{origin, origin, origin.Add(24 * time.Hour)}
	for i := 1; i <= 9; i++ {
		f.slots = append(f.slots, origin.Add(time.Duration(i)*15*time.Minute))
	}
	r := send(t, c, s, Intent{Kind: Availability, Date: "2030-04-08", PartySize: 3})
	if r.Text != "Horarios disponibles: 21:00, 21:15, 21:30, 21:45, 22:00, 22:15. Elegí uno para pedir una reserva." {
		t.Fatal(r)
	}
	for _, in := range []Intent{{Kind: Availability, Date: "2030-02-30", PartySize: 3}, {Kind: Availability, Date: "2030-04-08", PartySize: 31}} {
		if r := send(t, c, s, in); !strings.Contains(r.Text, "Elegí la fecha") {
			t.Fatal(r)
		}
	}
}
func TestConfiguredMenuAndCancellation(t *testing.T) {
	f, s, c := setup()
	f.info = VenueInfo{Policy: "Podés cancelar hasta dos horas antes.", Menu: &MenuPDF{Path: "/api/v1/public/reservas/local-demo/menu.pdf", Filename: "Carta.pdf"}}
	r := send(t, c, s, Intent{Kind: Menu})
	if r.Document == nil || *r.Document != (Document{"https://restaurant.example/api/v1/public/reservas/local-demo/menu.pdf", "Carta.pdf", "application/pdf"}) {
		t.Fatal(r)
	}
	for _, bad := range []string{"https://different.example/demo.pdf", "/api/v1/public/reservas/../carta.pdf", "/api/v1/public/reservas/local-demo/menu.pdf?token=secret", "/api/v1/public/reservas/carta.html", "//different.example/a.pdf"} {
		f.info.Menu.Path = bad
		if r := send(t, c, s, Intent{Kind: Menu}); r.Document != nil {
			t.Fatal(r)
		}
	}
	f.info.Menu.Path = "/api/v1/public/reservas/local-demo/menu.pdf"
	c.PublicOrigin = "http://restaurant.example"
	if r := send(t, c, s, Intent{Kind: Menu}); r.Document != nil {
		t.Fatal(r)
	}
	if r := send(t, c, s, Intent{Kind: CancellationPolicy}); r.Text != f.info.Policy {
		t.Fatal(r)
	}
	f.info.Policy = "secreto\x00"
	if r := send(t, c, s, Intent{Kind: CancellationPolicy}); r.Text != "Consultá al local por la política de cancelación." {
		t.Fatal(r)
	}
}
func TestPlainTextDoesNotBookAndFailuresDoNotLeak(t *testing.T) {
	for _, s := range []string{"¿Reservar?", "MENÚ", "¿Cómo cancelo?", "RESERVÁ HOY; horario a confirmar"} {
		if Classify(s) == BookingRequest {
			t.Fatal(s)
		}
	}
	if Classify("  ¿Mandame el MENÚ? ") != Menu || Classify("¿Cómo cancelo?") != CancellationPolicy || Classify("¿Hay lugar?") != Availability {
		t.Fatal("expected es-AR shortcuts")
	}
	f, s, c := setup()
	f.err = errors.New("internal data")
	if r := send(t, c, s, Intent{Kind: Help}); strings.Contains(r.Text, "internal data") {
		t.Fatal(r)
	}
	f.err = ErrClosed
	if r := send(t, c, s, Intent{Kind: Menu}); r.Text != "Este local no está disponible." {
		t.Fatal(r)
	}
	f.err = nil
	s.err = errors.New("fake sink stopped")
	if err := c.Handle(t.Context(), Intent{Kind: Help, Venue: "local-demo"}); !errors.Is(err, s.err) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	n := len(s.posted)
	if err := c.Handle(ctx, Intent{Kind: Help, Venue: "local-demo"}); !errors.Is(err, context.Canceled) || len(s.posted) != n {
		t.Fatal(err)
	}
}
