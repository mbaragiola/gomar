// Package publicwhatsapp replays one Meta-shaped guest event offline.
// It has no HTTP handler, callback authentication, credentials, or network.
package publicwhatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mbaragiola/gomar/app/internal/publicconcierge"
)

var ErrIgnored = errors.New("unsupported or unbound preview event")
var accountID = regexp.MustCompile(`^[0-9]{1,24}$`)
var wamid = regexp.MustCompile(`^[A-Za-z0-9._=-]{1,160}$`)
var slug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type Route struct{ BusinessID, PhoneNumberID, Venue string }
type Draft struct {
	MessagingProduct string         `json:"messaging_product"`
	To               string         `json:"to"`
	Type             string         `json:"type"`
	Text             *DraftText     `json:"text,omitempty"`
	Document         *DraftDocument `json:"document,omitempty"`
}
type DraftText struct {
	Body string `json:"body"`
}
type DraftDocument struct {
	Link     string `json:"link"`
	Filename string `json:"filename"`
	Caption  string `json:"caption"`
}
type previewEnvelope struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				MessagingProduct string `json:"messaging_product"`
				Metadata         struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []struct {
					ID   string `json:"id"`
					From string `json:"from"`
					Type string `json:"type"`
					Text struct {
						Body string `json:"body"`
					} `json:"text"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// Replay accepts only one event bound to the configured business, number,
// and venue. It returns a local draft for the sender bound by that event.
// Do not treat this parser as proof of callback authenticity.
func Replay(ctx context.Context, payload []byte, route Route, run func(context.Context, publicconcierge.Intent, publicconcierge.Sink) error) (Draft, error) {
	if err := ctx.Err(); err != nil {
		return Draft{}, err
	}
	if run == nil || !accountID.MatchString(route.BusinessID) || !accountID.MatchString(route.PhoneNumberID) || len(route.Venue) > 48 || !slug.MatchString(route.Venue) || len(payload) > 16<<10 || !utf8.Valid(payload) {
		return Draft{}, ErrIgnored
	}
	var env previewEnvelope
	if err := json.Unmarshal(payload, &env); err != nil || env.Object != "whatsapp_business_account" || len(env.Entry) != 1 {
		return Draft{}, ErrIgnored
	}
	entry := env.Entry[0]
	if entry.ID != route.BusinessID || len(entry.Changes) != 1 {
		return Draft{}, ErrIgnored
	}
	ch := entry.Changes[0]
	if ch.Field != "messages" || ch.Value.MessagingProduct != "whatsapp" || ch.Value.Metadata.PhoneNumberID != route.PhoneNumberID || len(ch.Value.Messages) != 1 {
		return Draft{}, ErrIgnored
	}
	msg := ch.Value.Messages[0]
	if msg.Type != "text" || !accountID.MatchString(msg.From) || !wamid.MatchString(msg.ID) || len(msg.Text.Body) > 320 || strings.TrimSpace(msg.Text.Body) == "" {
		return Draft{}, ErrIgnored
	}
	// Hash the validated account and event before handing the ID to the app.
	// No recipient or provider event ID is included in the concierge Intent.
	digest := sha256.Sum256([]byte(route.BusinessID + "\x00" + route.PhoneNumberID + "\x00" + msg.ID))
	in := intent(msg.Text.Body)
	in.Venue = route.Venue
	in.EventID = hex.EncodeToString(digest[:])
	capture := draftSink{to: msg.From, includeHelp: in.Kind == publicconcierge.Help || in.Kind == publicconcierge.Availability && in.Date == ""}
	if err := run(ctx, in, &capture); err != nil {
		return Draft{}, err
	}
	if !capture.ready {
		return Draft{}, errors.New("preview produced no reply")
	}
	return capture.draft, nil
}

func intent(raw string) publicconcierge.Intent {
	left, name, hasName := strings.Cut(raw, "|")
	fields := strings.Fields(strings.ToLower(left))
	if !hasName && len(fields) == 3 && fields[0] == "disponibilidad" {
		n, err := strconv.Atoi(fields[2])
		if err == nil && len(fields[1]) == 10 {
			return publicconcierge.Intent{Kind: publicconcierge.Availability, Date: fields[1], PartySize: n}
		}
	}
	if hasName && len(fields) == 5 && fields[0] == "confirmo" && fields[1] == "reserva" && len(fields[2]) == 10 && len(fields[3]) == 5 {
		loc, err := time.LoadLocation("America/Argentina/Buenos_Aires")
		if err == nil {
			when, err := time.ParseInLocation("2006-01-02 15:04", fields[2]+" "+fields[3], loc)
			party, nerr := strconv.Atoi(fields[4])
			name = strings.TrimSpace(name)
			if err == nil && nerr == nil && name != "" && when.Format("2006-01-02 15:04") == fields[2]+" "+fields[3] {
				return publicconcierge.Intent{Kind: publicconcierge.BookingRequest, StartAt: when.Format(time.RFC3339), PartySize: party, GuestName: name, ConfirmedByGuest: true}
			}
		}
		return publicconcierge.Intent{Kind: publicconcierge.Help}
	}
	return publicconcierge.Intent{Kind: publicconcierge.Classify(raw)}
}

type draftSink struct {
	to                 string
	includeHelp, ready bool
	draft              Draft
}

const instructions = "\n\nPara consultar: disponibilidad AAAA-MM-DD cantidad\nPara enviar la solicitud: confirmo reserva AAAA-MM-DD HH:MM cantidad | nombre"

func (s *draftSink) Publish(ctx context.Context, r publicconcierge.Reply) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ready || !utf8.ValidString(r.Text) || len(r.Text) == 0 || len(r.Text) > 2048 {
		return errors.New("invalid preview reply")
	}
	d := Draft{MessagingProduct: "whatsapp", To: s.to, Type: "text"}
	if r.Document == nil {
		if s.includeHelp && r.Text != "Este local no está disponible." {
			r.Text += instructions
		}
		d.Text = &DraftText{r.Text}
	} else {
		// The concierge validates trusted same-origin metadata. Defense here
		// rejects unsupported shapes; the preview never fetches the URL.
		u, err := url.Parse(r.Document.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || r.Document.ContentType != "application/pdf" || r.Document.Filename != "menu.pdf" {
			return errors.New("invalid preview document")
		}
		d.Type = "document"
		d.Document = &DraftDocument{u.String(), r.Document.Filename, r.Text}
	}
	s.ready = true
	s.draft = d
	return nil
}

// Avoid yielding a partial draft from failed or cancelled processing.
func (d Draft) JSON() ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(d); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
