package fiscalcontroller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Controller, *Simulator, Request) {
	t.Helper()
	s := NewSimulator(7)
	c, err := New(s, Config{PointOfSale: 7, Capabilities: Capabilities{FiscalTicket: true, TicketInvoiceB: true}})
	if err != nil {
		t.Fatal(err)
	}
	return c, s, Request{"sale-1", FiscalTicket, 10000}
}

func TestValidationAndIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	c, s, req := fixture(t)
	for _, invalid := range []struct {
		request Request
		err     error
	}{
		{Request{"sale-1", Kind("table_check"), 100}, ErrUnsupported},
		{Request{"sale-1", TicketInvoiceA, 100}, ErrUnsupported},
		{Request{"", FiscalTicket, 100}, ErrInvalid},
		{Request{"sale-1", FiscalTicket, 0}, ErrInvalid},
	} {
		if _, err := c.Issue(ctx, invalid.request); !errors.Is(err, invalid.err) {
			t.Errorf("Issue(%+v) error = %v, want %v", invalid.request, err, invalid.err)
		}
	}
	if s.lastNumber != 0 {
		t.Fatalf("invalid requests reached device: %d documents", s.lastNumber)
	}
	want := Result{Issued, Document{req.SaleID, req.Kind, 7, 1, req.TotalCentavos}}
	if got, err := c.Issue(ctx, req); err != nil || got != want {
		t.Fatalf("first issue = %+v, %v; want %+v", got, err, want)
	}
	if got, err := c.Issue(ctx, req); err != nil || got != want {
		t.Fatalf("replay = %+v, %v; want %+v", got, err, want)
	}
	changed := req
	changed.TotalCentavos++
	if _, err := c.Issue(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request error = %v, want conflict", err)
	}
	if s.lastNumber != 1 {
		t.Fatalf("device issued %d documents, want 1", s.lastNumber)
	}
}

func TestLostAcknowledgmentBlocksDeviceUntilFound(t *testing.T) {
	ctx := context.Background()
	c, s, req := fixture(t)
	s.FailNextIssue(LoseResponseAfterIssue)
	if got, err := c.Issue(ctx, req); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("lost response = %+v, %v", got, err)
	}
	s.SetLookupInconclusive(true)
	if got, err := c.Reconcile(ctx, req.SaleID); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("inconclusive lookup = %+v, %v", got, err)
	}
	other := Request{"sale-2", FiscalTicket, 20000}
	for _, blocked := range []Request{req, other} {
		if _, err := c.Issue(ctx, blocked); !errors.Is(err, ErrNeedsReconciliation) {
			t.Fatalf("issue(%+v) error = %v, want reconciliation", blocked, err)
		}
	}
	if byWrongKey, err := c.Reconcile(ctx, other.SaleID); byWrongKey.State != NoAttempt || !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("unattempted lookup = %+v, %v", byWrongKey, err)
	}
	s.SetLookupInconclusive(false)
	want := Result{Issued, Document{req.SaleID, req.Kind, 7, 1, req.TotalCentavos}}
	if got, err := c.Reconcile(ctx, req.SaleID); err != nil || got != want {
		t.Fatalf("reconciled = %+v, %v; want %+v", got, err, want)
	}
	if got, err := c.Issue(ctx, req); err != nil || got != want {
		t.Fatalf("reissue = %+v, %v; want original document", got, err)
	}
	if got, err := c.Issue(ctx, other); err != nil || got.State != Issued || got.Document.Number != 2 {
		t.Fatalf("next issue = %+v, %v", got, err)
	}
	if s.lastNumber != 2 {
		t.Fatalf("device issued %d documents, want 2", s.lastNumber)
	}
}

func TestOnlyExplicitRetryAfterConfirmedAbsence(t *testing.T) {
	ctx := context.Background()
	c, s, req := fixture(t)
	s.FailNextIssue(LoseResponseBeforeIssue)
	if got, err := c.Issue(ctx, req); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("first issue = %+v, %v", got, err)
	}
	if got, err := c.Reconcile(ctx, req.SaleID); got.State != ConfirmedNotIssued || err != nil {
		t.Fatalf("reconcile = %+v, %v", got, err)
	}
	if s.lastNumber != 0 {
		t.Fatal("lookup performed an issue")
	}
	changed := req
	changed.Kind = TicketInvoiceB
	if _, err := c.Issue(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry = %v, want conflict", err)
	}
	if got, err := c.Issue(ctx, req); got.State != Issued || err != nil || got.Document.Number != 1 {
		t.Fatalf("explicit retry = %+v, %v", got, err)
	}
}

type scripted struct {
	issue  func(context.Context, Request) (Document, error)
	lookup func(context.Context, Request) (LookupResult, error)
}

func (d scripted) Issue(ctx context.Context, req Request) (Document, error) { return d.issue(ctx, req) }
func (d scripted) Lookup(ctx context.Context, req Request) (LookupResult, error) {
	return d.lookup(ctx, req)
}

func TestUnmatchedOrConflictingEvidenceKeepsDeviceBlocked(t *testing.T) {
	ctx := context.Background()
	req := Request{"sale-1", FiscalTicket, 100}
	wrongDoc := Document{req.SaleID, req.Kind, 8, 1, req.TotalCentavos}
	for _, observed := range []LookupResult{
		{Found, wrongDoc},
		{Found, Document{"different-sale", FiscalTicket, 7, 1, req.TotalCentavos}},
		{Found, Document{req.SaleID, TicketInvoiceA, 7, 1, req.TotalCentavos}},
		{Found, Document{req.SaleID, FiscalTicket, 7, 0, req.TotalCentavos}},
		{NotIssued, wrongDoc},
		{LookupState(99), Document{}},
	} {
		var calls int
		c, err := New(scripted{
			issue:  func(context.Context, Request) (Document, error) { calls++; return wrongDoc, nil },
			lookup: func(context.Context, Request) (LookupResult, error) { return observed, nil },
		}, Config{7, Capabilities{FiscalTicket: true}})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := c.Issue(ctx, req); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
			t.Fatalf("wrong issue result = %+v, %v", got, err)
		}
		if got, err := c.Reconcile(ctx, req.SaleID); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
			t.Fatalf("weak proof (%+v) = %+v, %v", observed, got, err)
		}
		if _, err := c.Issue(ctx, req); !errors.Is(err, ErrNeedsReconciliation) || calls != 1 {
			t.Fatalf("retry after weak proof: error = %v, calls = %d", err, calls)
		}
	}
}

func TestSingleDeviceSerializesWhileAnotherDeviceCanIssue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started, release := make(chan struct{}, 2), make(chan struct{})
	defer close(release)
	var calls atomic.Int64
	c, err := New(scripted{issue: func(ctx context.Context, req Request) (Document, error) {
		n := calls.Add(1)
		started <- struct{}{}
		if n == 1 {
			select {
			case <-release:
			case <-ctx.Done():
				return Document{}, ctx.Err()
			}
		}
		return Document{req.SaleID, req.Kind, 7, n, req.TotalCentavos}, nil
	}}, Config{7, Capabilities{FiscalTicket: true}})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{"sale-1", FiscalTicket, 100}
	done := make(chan error, 1)
	go func() { _, err := c.Issue(ctx, req); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first request did not reach device")
	}

	// The next request should wait for its deadline rather than bill while the
	// first is outstanding. Check transport calls, not just the returned error.
	other := Request{"sale-2", FiscalTicket, 200}
	short, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	defer stop()
	if _, err := c.Issue(short, other); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued canceled request returned %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("issued %d device calls while first is blocked", n)
	}

	independent, err := New(NewSimulator(8), Config{8, Capabilities{FiscalTicket: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := independent.Issue(ctx, other); got.State != Issued || err != nil {
		t.Fatalf("independent device = %+v, %v", got, err)
	}

	// Cancellation while sending is unsafe to retry even when the local caller
	// gave up; it must be reconciled as any other transport error.
	cancel()
	if err := <-done; !errors.Is(err, ErrNeedsReconciliation) || !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("first issue error = %v", err)
	}
}

func TestDocumentNumberCannotBeReusedWithinKindAndDevice(t *testing.T) {
	ctx := context.Background()
	repeatedDoc := func(req Request) Document { return Document{req.SaleID, req.Kind, 7, 1, req.TotalCentavos} }
	c, err := New(scripted{
		issue: func(_ context.Context, req Request) (Document, error) { return repeatedDoc(req), nil },
		lookup: func(_ context.Context, req Request) (LookupResult, error) {
			return LookupResult{Found, repeatedDoc(req)}, nil
		},
	}, Config{7, Capabilities{FiscalTicket: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Issue(ctx, Request{"sale-1", FiscalTicket, 100}); got.State != Issued || err != nil {
		t.Fatalf("first issue = %+v, %v", got, err)
	}
	if got, err := c.Issue(ctx, Request{"sale-2", FiscalTicket, 100}); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("duplicate number = %+v, %v", got, err)
	}
	if got, err := c.Reconcile(ctx, "sale-2"); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("conflicting lookup proof = %+v, %v", got, err)
	}
}

func TestAmountMismatchDoesNotSettleOrUnlockDevice(t *testing.T) {
	ctx := context.Background()
	req := Request{"sale-1", FiscalTicket, 12345}
	other := Request{"sale-2", FiscalTicket, 20000}
	actual := Document{req.SaleID, req.Kind, 7, 1, req.TotalCentavos + 1}
	lookedUp := actual
	calls := 0
	c, err := New(scripted{
		issue:  func(context.Context, Request) (Document, error) { calls++; return actual, nil },
		lookup: func(context.Context, Request) (LookupResult, error) { return LookupResult{Found, lookedUp}, nil },
	}, Config{7, Capabilities{FiscalTicket: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Issue(ctx, req); got != (Result{State: OutcomeUnknown}) || !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("wrong issue total settled: %+v, %v", got, err)
	}
	for _, amount := range []int64{actual.TotalCentavos, 0} {
		lookedUp.TotalCentavos = amount
		if got, err := c.Reconcile(ctx, req.SaleID); got != (Result{State: OutcomeUnknown}) || !errors.Is(err, ErrNeedsReconciliation) {
			t.Errorf("wrong/missing lookup total %d settled: %+v, %v", amount, got, err)
		}
		for _, blocked := range []Request{req, other} {
			if _, err := c.Issue(ctx, blocked); !errors.Is(err, ErrNeedsReconciliation) {
				t.Errorf("issue(%+v) after weak proof returned %v", blocked, err)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("wrong totals allowed %d submissions; want only initial submission", calls)
	}
	// The proof below is independent, not manufactured by swapping request
	// fields into a transport response. It models later matching evidence.
	lookedUp.TotalCentavos = req.TotalCentavos
	if got, err := c.Reconcile(ctx, req.SaleID); got != (Result{Issued, lookedUp}) || err != nil {
		t.Fatalf("matching lookup = %+v, %v", got, err)
	}
	if got, err := c.Issue(ctx, req); got.Document != lookedUp || err != nil || calls != 1 {
		t.Fatalf("issued replay = %+v, %v; calls = %d", got, err, calls)
	}
}
