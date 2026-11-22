package whatsappchat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type fakeSink struct {
	got  [][2]string
	fail bool
}

func (s *fakeSink) Reply(_ context.Context, recipient, body string) error {
	s.got = append(s.got, [2]string{recipient, body})
	if s.fail {
		return errors.New("private simulator detail")
	}
	return nil
}

func TestSyntheticExchange(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{}
	bot, err := NewSynthetic(sink)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []Message{
		{"fixture-other", "same", query},
		{"fixture-centro", "same", "cuánto debemos en norte"},
		{"fixture-centro", "same", " Cuánto debemos "},
		{"fixture-centro", "same", query},
		{"fixture-norte", "same", query},
	} {
		if err := bot.Handle(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	want := [][2]string{
		{"fixture-centro", "Datos de prueba. Saldos pendientes:\nARS 160,00 pendientes; 125,00 vencidos.\nUSD 4,50 pendientes; 4,50 vencidos."},
		{"fixture-norte", "Datos de prueba. Saldos pendientes:\nARS 71,00 pendientes; 0,00 vencidos."},
	}
	if !reflect.DeepEqual(sink.got, want) {
		t.Fatalf("unexpected fake replies: %v", sink.got)
	}
}

func TestSyntheticFailureAndJournalLimit(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{fail: true}
	bot, err := NewSynthetic(sink)
	if err != nil {
		t.Fatal(err)
	}
	msg := Message{"fixture-centro", "simulated-failure", query}
	if err := bot.Handle(ctx, msg); err != errUnavailable {
		t.Fatal(err)
	}
	if err := bot.Handle(ctx, msg); err != nil || len(sink.got) != 1 {
		t.Fatalf("duplicate attempt: %v", err)
	}
	sink.fail = false
	for i := 1; i < maxMessages; i++ {
		if err := bot.Handle(ctx, Message{"fixture-centro", fmt.Sprint(i), query}); err != nil {
			t.Fatal(err)
		}
	}
	if err := bot.Handle(ctx, Message{"fixture-centro", "new", query}); err != errFull || len(sink.got) != maxMessages {
		t.Fatalf("journal overflow: %v", err)
	}
}
