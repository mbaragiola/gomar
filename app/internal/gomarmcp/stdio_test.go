package gomarmcp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mbaragiola/gomar/app/internal/payablechat"
)

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"local-test","version":"1"}}}` + "\n"
const ready = `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
const toolCall = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_payables_summary","arguments":{}}}` + "\n"

type readerFunc func(context.Context) (payablechat.Summary, error)

func (r readerFunc) Summary(ctx context.Context) (payablechat.Summary, error) { return r(ctx) }

func TestServeBoundsReaderAndHidesErrors(t *testing.T) {
	const rejected = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_payables_summary","arguments":{"alcance":"otro"}}}` + "\n"
	requests := initialize + toolCall + ready + rejected + toolCall
	var out bytes.Buffer
	calls := 0
	reader := readerFunc(func(ctx context.Context) (payablechat.Summary, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > readTimeout {
			t.Fatal("reader deadline missing or exceeds the transport ceiling")
		}
		return payablechat.Summary{}, errors.New("private diagnostic")
	})
	if err := Serve(context.Background(), strings.NewReader(requests), &out, reader); err != nil {
		t.Fatal(err)
	}
	result := out.String()
	if calls != 1 || !strings.Contains(result, "Server not ready") || strings.Count(result, `"isError":true`) != 2 ||
		strings.Contains(result, "private diagnostic") {
		t.Fatalf("reader or error fencing failed: calls=%d, output=%q", calls, result)
	}
}

func TestServeHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	reader := readerFunc(func(call context.Context) (payablechat.Summary, error) {
		cancel()
		if !errors.Is(call.Err(), context.Canceled) {
			t.Fatal("parent canceled but reader context remained open")
		}
		return payablechat.Summary{Scope: "discard this result"}, nil
	})
	err := Serve(ctx, strings.NewReader(initialize+ready+toolCall), &out, reader)
	if !errors.Is(err, context.Canceled) || strings.Contains(out.String(), "discard this result") ||
		!strings.Contains(out.String(), `"isError":true`) {
		t.Fatalf("cancellation leaked a stale return: err=%v output=%q", err, out.String())
	}
}
