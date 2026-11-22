// Package publicconcierge builds public restaurant replies without a provider.
package publicconcierge

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Kind uint8

const (
	Help Kind = iota
	Availability
	BookingRequest
	Menu
	CancellationPolicy
)

// Intent is assembled from validated message selections. Open text can only
// ask for information or guidance; a booking needs a separate confirmation.
type Intent struct {
	Kind                                     Kind
	Venue, Date, StartAt, GuestName, EventID string
	PartySize                                int
	ConfirmedByGuest                         bool
}

type MenuPDF struct{ Path, Filename string }
type VenueInfo struct {
	Policy string
	Menu   *MenuPDF
}
type Request struct {
	StartAt            time.Time
	PartySize          int
	GuestName, EventID string
}
type Pending struct {
	Reference string
	Replay    bool
}
type Document struct{ URL, Filename, ContentType string }
type Reply struct {
	Text     string
	Document *Document
}

// Catalog must expose only explicitly enabled public venues. Times are the
// start of restaurant slots; Request must enforce durable event idempotency
// scoped to the venue and return only requests which await confirmation.
type Catalog interface {
	Info(context.Context, string) (VenueInfo, error)
	Available(context.Context, string, string, int) ([]time.Time, error)
	Request(context.Context, string, Request) (Pending, error)
}

// Sink is bound to the audience by its caller. This package never sees a
// provider address, token, message envelope, or account.
type Sink interface {
	Publish(context.Context, Reply) error
}

type Concierge struct {
	Catalog      Catalog
	Sink         Sink
	PublicOrigin string
}

var (
	ErrClosed      = errors.New("public venue unavailable")
	ErrUnavailable = errors.New("slot unavailable")
	venueSlug      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$`)
	token          = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	menuPath       = regexp.MustCompile(`^/api/v1/public/reservas/[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?/menu\.pdf$`)
	menuFilename   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,59}\.pdf$`)
)

// Classify recognizes bounded common es-AR help commands. In particular it
// never infers a mutating operation or assumes ambiguous dates from text.
func Classify(raw string) Kind {
	if len(raw) > 160 {
		return Help
	}
	s := strings.Join(strings.Fields(strings.ToLower(raw)), " ")
	s = strings.Trim(s, " ¿?¡!.")
	switch s {
	case "menú", "menu", "carta", "mandame el menú", "mandame la carta":
		return Menu
	case "cancelación", "cancelacion", "política de cancelación", "como cancelo", "cómo cancelo":
		return CancellationPolicy
	case "reservar", "reserva", "reservas", "hay lugar", "disponibilidad", "mesa":
		return Availability
	default:
		return Help
	}
}

func (c Concierge) Handle(ctx context.Context, in Intent) error {
	if c.Catalog == nil || c.Sink == nil {
		return errors.New("concierge not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !venueSlug.MatchString(in.Venue) {
		return c.send(ctx, Reply{Text: "Este local no está disponible."})
	}
	info, err := c.Catalog.Info(ctx, in.Venue)
	if err != nil {
		return c.problem(ctx, err)
	}
	switch in.Kind {
	case Menu:
		if doc := document(c.PublicOrigin, info.Menu); doc != nil {
			return c.send(ctx, Reply{Text: "Acá tenés el menú.", Document: doc})
		}
		return c.send(ctx, Reply{Text: "El menú todavía no está disponible por acá."})
	case CancellationPolicy:
		policy := cleanPolicy(info.Policy)
		if policy == "" {
			policy = "Consultá al local por la política de cancelación."
		}
		return c.send(ctx, Reply{Text: policy})
	case Availability:
		if !validDate(in.Date) || !validParty(in.PartySize) {
			return c.send(ctx, Reply{Text: "Elegí la fecha y decime cuántas personas son."})
		}
		slots, err := c.Catalog.Available(ctx, in.Venue, in.Date, in.PartySize)
		if err != nil {
			return c.problem(ctx, err)
		}
		return c.send(ctx, Reply{Text: slotText(in.Date, slots)})
	case BookingRequest:
		if !in.ConfirmedByGuest || !token.MatchString(in.EventID) || !validParty(in.PartySize) || !validName(in.GuestName) || len(in.StartAt) > 35 {
			return c.send(ctx, Reply{Text: "Elegí un horario, indicá tu nombre y confirmá que querés enviar la solicitud."})
		}
		start, err := time.Parse(time.RFC3339, in.StartAt)
		if err != nil {
			return c.send(ctx, Reply{Text: "Elegí un horario de la lista para enviar la solicitud."})
		}
		pending, err := c.Catalog.Request(ctx, in.Venue, Request{start, in.PartySize, strings.TrimSpace(in.GuestName), in.EventID})
		if err != nil {
			return c.problem(ctx, err)
		}
		if !token.MatchString(pending.Reference) {
			return c.problem(ctx, errors.New("invalid pending response"))
		}
		if pending.Replay {
			return c.send(ctx, Reply{Text: "Ya recibimos esa solicitud. Consultá al local por su estado."})
		}
		return c.send(ctx, Reply{Text: "Recibimos tu solicitud. La mesa todavía no está confirmada."})
	default:
		return c.send(ctx, Reply{Text: "Podés consultar horarios para reservar, pedir el menú o ver la política de cancelación."})
	}
}

func (c Concierge) send(ctx context.Context, r Reply) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Sink.Publish(ctx, r)
}
func (c Concierge) problem(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, ErrClosed):
		return c.send(ctx, Reply{Text: "Este local no está disponible."})
	case errors.Is(err, ErrUnavailable):
		return c.send(ctx, Reply{Text: "Ese horario no está disponible. Elegí otro."})
	default:
		return c.send(ctx, Reply{Text: "No pudimos completar la consulta. Probá de nuevo más tarde."})
	}
}
func validDate(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}
func validParty(n int) bool { return n >= 1 && n <= 12 }
func validName(s string) bool {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || len(s) > 200 || utf8.RuneCountInString(s) < 2 || utf8.RuneCountInString(s) > 60 {
		return false
	}
	letter := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			letter = true
			continue
		}
		if !(unicode.IsMark(r) || r == ' ' || r == '-' || r == '\'' || r == '’' || r == '.') {
			return false
		}
	}
	return letter
}
func cleanPolicy(s string) string {
	if len(s) > 1600 || !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' || unicode.In(r, unicode.Cf) }) >= 0 {
		return ""
	}
	return strings.TrimSpace(s)
}
func slotText(date string, slots []time.Time) string {
	// This build uses the same Argentina venue time zone as the booking service.
	zone, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		return "No pudimos consultar los horarios. Probá de nuevo más tarde."
	}
	var times []string
	seen := map[string]bool{}
	// Limit inspected data as well as displayed data.
	for _, slot := range slots[:min(len(slots), 64)] {
		local := slot.In(zone)
		clock := local.Format("15:04")
		if local.Format(time.DateOnly) != date || seen[clock] {
			continue
		}
		times = append(times, clock)
		seen[clock] = true
		if len(times) == 6 {
			break
		}
	}
	if len(times) == 0 {
		return "No hay horarios para esa fecha y cantidad de personas."
	}
	return "Horarios disponibles: " + strings.Join(times, ", ") + ". Elegí uno para pedir una reserva."
}
func document(origin string, menu *MenuPDF) *Document {
	if menu == nil || !menuPath.MatchString(menu.Path) || !menuFilename.MatchString(menu.Filename) {
		return nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || !strings.Contains(u.Hostname(), ".") || u.Hostname() != u.Host || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return nil
	}
	u.Path = menu.Path
	return &Document{u.String(), menu.Filename, "application/pdf"}
}
