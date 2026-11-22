// Package whatsapppreview replays a fabricated WhatsApp event from a local reader.
package whatsapppreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mbaragiola/gomar/internal/preview"
)

const (
	payloadLimit   = 4096
	fixtureAccount = "demo-business"
	fixtureNumber  = "demo-destination"
	fixtureSender  = "demo-sender"
	fixtureID      = "wamid.demo-message"
)

var invalid = errors.New("invalid synthetic WhatsApp event")

type message struct {
	From string `json:"from"`
	ID   string `json:"id"`
	Type string `json:"type"`
	Text *struct {
		Body string `json:"body"`
	} `json:"text"`
}

type envelope struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Product  string `json:"messaging_product"`
				Metadata struct {
					Number string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []message         `json:"messages"`
				Statuses []json.RawMessage `json:"statuses"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// Replay reads a single fabricated envelope through EOF. A matching text gets
// one preview on out; supported non-query events leave out untouched. It never
// sends a message or validates a provider signature.
func Replay(ctx context.Context, in io.Reader, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(in, payloadLimit+1))
	if err != nil || len(raw) > payloadLimit {
		return invalid
	}
	var event envelope
	if err := json.Unmarshal(raw, &event); err != nil || event.Object != "whatsapp_business_account" || len(event.Entry) != 1 {
		return invalid
	}
	entry := event.Entry[0]
	if entry.ID != fixtureAccount || len(entry.Changes) != 1 {
		return invalid
	}
	change := entry.Changes[0]
	value := change.Value
	if change.Field != "messages" || value.Product != "whatsapp" || value.Metadata.Number != fixtureNumber {
		return invalid
	}
	if len(value.Messages) == 0 && len(value.Statuses) == 1 {
		return nil
	}
	if len(value.Messages) != 1 || len(value.Statuses) != 0 {
		return invalid
	}
	msg := value.Messages[0]
	if msg.From == "" || msg.ID == "" || msg.Type == "" {
		return invalid
	}
	if msg.From != fixtureSender || msg.ID != fixtureID || msg.Type != "text" {
		return nil
	}
	if msg.Text == nil || len(msg.Text.Body) > 64 {
		return invalid
	}
	if !strings.EqualFold(strings.TrimSpace(msg.Text.Body), "cuánto debemos") {
		return nil
	}
	totals, err := preview.DemoSummary()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lines := []string{"Datos de prueba. Saldos pendientes:"}
	for _, t := range totals.Totals {
		lines = append(lines, fmt.Sprintf("%s %s pendientes; %s vencidos.", t.Currency, amount(t.Balance), amount(t.OverdueBalance)))
	}
	reply := struct {
		Synthetic bool   `json:"datos_sinteticos"`
		Text      string `json:"respuesta"`
	}{true, strings.Join(lines, "\n")}
	return json.NewEncoder(out).Encode(reply)
}

func amount(cents int64) string {
	return fmt.Sprintf("%d,%02d", cents/100, cents%100)
}
