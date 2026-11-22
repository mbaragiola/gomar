// Package whatsappchat models a WhatsApp exchange with generated data and a
// fake reply sink. It contains no provider transport or account integration.
package whatsappchat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mbaragiola/gomar/app/internal/payablechat"
)

const query = "cuánto debemos"
const maxMessages = 128

var (
	errFull        = errors.New("synthetic message journal is full")
	errUnavailable = errors.New("synthetic summary or reply is unavailable")
)

// Message contains synthetic identifiers only: Sender is not a phone number.
type Message struct {
	Sender string
	ID     string
	Text   string
}

type ReplySink interface {
	Reply(context.Context, string, string) error
}

type Adapter struct {
	bindings map[string]payablechat.Reader
	sink     ReplySink
	mu       sync.Mutex
	seen     map[[2]string]struct{}
}

func NewSynthetic(sink ReplySink) (*Adapter, error) {
	if sink == nil {
		return nil, errors.New("synthetic reply sink required")
	}
	bindings := make(map[string]payablechat.Reader)
	for sender, scope := range map[string]string{"fixture-centro": "centro", "fixture-norte": "norte"} {
		reader, err := payablechat.NewSynthetic(scope)
		if err != nil {
			return nil, err
		}
		bindings[sender] = reader
	}
	return &Adapter{bindings: bindings, sink: sink, seen: make(map[[2]string]struct{})}, nil
}

// Handle ignores unbound senders and supports one exact phrase. The bounded
// journal allows only one attempt per sender and ID during this adapter's life.
// A failed attempt stays marked because a reply sink may have accepted it.
func (a *Adapter) Handle(ctx context.Context, msg Message) error {
	reader, ok := a.bindings[msg.Sender]
	if !ok || len(msg.ID) == 0 || len(msg.ID) > 64 || len(msg.Text) > 64 || !strings.EqualFold(strings.TrimSpace(msg.Text), query) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := [2]string{msg.Sender, msg.ID}
	a.mu.Lock()
	if _, seen := a.seen[key]; seen {
		a.mu.Unlock()
		return nil
	}
	if len(a.seen) >= maxMessages {
		a.mu.Unlock()
		return errFull
	}
	a.seen[key] = struct{}{}
	a.mu.Unlock()
	summary, err := reader.Summary(ctx)
	if err != nil || !summary.Synthetic {
		return errUnavailable
	}
	lines := []string{"Datos de prueba. Saldos pendientes:"}
	for _, total := range summary.Totals {
		if (total.Currency != "ARS" && total.Currency != "USD") || total.OutstandingCents < 0 || total.OverdueCents < 0 {
			return errUnavailable
		}
		lines = append(lines, fmt.Sprintf("%s %s pendientes; %s vencidos.", total.Currency, amount(total.OutstandingCents), amount(total.OverdueCents)))
	}
	if len(lines) == 1 {
		lines = append(lines, "No hay saldos pendientes.")
	}
	if err := a.sink.Reply(ctx, msg.Sender, strings.Join(lines, "\n")); err != nil {
		return errUnavailable
	}
	return nil
}

func amount(cents int64) string {
	return fmt.Sprintf("%d,%02d", cents/100, cents%100)
}
