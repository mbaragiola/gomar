package tenantfiscalpreview

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestSyntheticOutboxPostgres(t *testing.T) {
	dsn := os.Getenv("GOMAR_FISCAL_OUTBOX_TEST_DSN")
	if dsn == "" {
		t.Skip("requires a fresh disposable fiscal fixture database")
	}
	ctx := t.Context()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := OpenOutbox(ctx, db); !errors.Is(err, ErrFixtureDB) {
		t.Fatalf("open before bootstrap: %v", err)
	}
	o, err := BootstrapOutbox(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	p, op, s := fixture()
	if err := o.Configure(ctx, p); err != nil {
		t.Fatal(err)
	}
	sibling := p
	sibling.Scope.OutletID++
	sibling.Issuer = "fixture-sibling"
	if err := o.Configure(ctx, sibling); err != nil {
		t.Fatal(err)
	}
	foreign := p
	foreign.Scope.EntityID++
	foreign.Issuer = "fixture-other"
	if err := o.Configure(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	duplicateIssuer := p
	duplicateIssuer.Scope.OutletID += 20
	if err := o.Configure(ctx, duplicateIssuer); err == nil {
		t.Fatal("same issuer point configured twice")
	}

	approved := confirmFixture(t, p, op, s)
	const parallel = 12
	ids := make(chan int64, parallel)
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := o.Enqueue(ctx, approved)
			if err != nil {
				t.Error(err)
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	var original int64
	for id := range ids {
		if original == 0 {
			original = id
		}
		if id != original || id == 0 {
			t.Fatalf("multiple intent IDs: %d, %d", original, id)
		}
	}
	changed := s
	changed.TotalMinor++
	if _, err := o.Enqueue(ctx, confirmFixture(t, p, op, changed)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("altered replay: %v", err)
	}
	if _, err := o.Enqueue(ctx, Confirmed{}); !errors.Is(err, ErrAttempt) {
		t.Fatalf("unapproved: %v", err)
	}
	second := s
	second.ID++
	otherID, err := o.Enqueue(ctx, confirmFixture(t, p, op, second))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan Claim, parallel)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := o.ClaimNext(ctx, p, time.Minute)
			if err == nil {
				results <- c
			} else if !errors.Is(err, ErrBusy) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(results)
	var claim Claim
	count := 0
	for c := range results {
		claim = c
		count++
	}
	if count != 1 || claim.ID != original || claim.Attempt.Payload().Key.Number != 1 {
		t.Fatalf("claimed %d, wrong identity/number: %d", count, claim.ID)
	}
	bad := claim
	bad.token = "ffffffffffffffffffffffffffffffff"
	if _, err := o.AfterDispatch(ctx, bad, good(claim.Attempt)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("wrong fence: %v", err)
	}
	if _, err := o.UnknownAttempt(ctx, foreign, original); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign: %v", err)
	}
	if _, err := o.UnknownAttempt(ctx, sibling, original); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("sibling: %v", err)
	}

	// Persist the issue in a fake provider and then drop the response, exactly
	// where a real process could exit. Open a separate connection pool to prove
	// no worker-local memory can restore or reclaim the PostgreSQL claim.
	provider := fakeWSFE{records: make(map[Key]Reply)}
	provider.submitButLoseResponse(claim.Attempt)
	restartedDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedDB.Close()
	restarted, err := OpenOutbox(ctx, restartedDB)
	if err != nil {
		t.Fatal(err)
	}
	assertBusy(t, ctx, restarted, p)
	expire(t, ctx, restartedDB, original)
	if _, err := o.AfterDispatch(ctx, claim, good(claim.Attempt)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("expired result accepted: %v", err)
	}
	assertBusy(t, ctx, restarted, p)
	if _, err := restarted.UnknownAttempt(ctx, foreign, original); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign unknown: %v", err)
	}
	if _, err := restarted.UnknownAttempt(ctx, sibling, original); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("sibling unknown: %v", err)
	}
	a, err := restarted.UnknownAttempt(ctx, p, original)
	if err != nil || a != claim.Attempt {
		t.Fatalf("persisted attempt drift: %v", err)
	}
	if got, err := restarted.Reconcile(ctx, original, a, Reply{Outcome: NotFound}); got != Unknown || err != nil {
		t.Fatalf("absent lookup: %v %v", got, err)
	}
	assertBusy(t, ctx, restarted, p)
	if got, err := restarted.Reconcile(ctx, original, a, provider.lookup(a.Payload().Key)); got != Authorized || err != nil || provider.calls != 1 {
		t.Fatalf("lookup did not authorize one dispatch: %v %v", got, err)
	}
	if _, err := restarted.Reconcile(ctx, original, a, good(a)); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("replayed resolution: %v", err)
	}
	next, err := restarted.ClaimNext(ctx, p, time.Minute)
	if err != nil || next.ID != otherID || next.Attempt.Payload().Key.Number != 2 {
		t.Fatalf("next claim: %v %v", next, err)
	}

	// Crash after reservation but before fake I/O: no invented approval or
	// replacement dispatch. Only the same unknown number can be reconciled.
	expire(t, ctx, db, next.ID)
	assertBusy(t, ctx, restarted, p)
	a, err = restarted.UnknownAttempt(ctx, p, otherID)
	if err != nil || a != next.Attempt {
		t.Fatalf("second persisted attempt drift: %v", err)
	}
	if got, err := restarted.Reconcile(ctx, otherID, a, provider.lookup(a.Payload().Key)); got != Unknown || err != nil {
		t.Fatalf("non-dispatched lookup: %v %v", got, err)
	}
	mismatch := good(a)
	mismatch.Echo.Key.PointOfSale++
	if got, err := restarted.Reconcile(ctx, otherID, a, mismatch); got != NeedsReview || err != nil {
		t.Fatalf("mismatch: %v %v", got, err)
	}
	assertBusy(t, ctx, restarted, p)

	// A held channel never blocks an independent outlet's numbering.
	siblingOp, siblingSale := op, s
	siblingOp.Scope, siblingSale.Scope = sibling.Scope, sibling.Scope
	id, err := restarted.Enqueue(ctx, confirmFixture(t, sibling, siblingOp, siblingSale))
	if err != nil {
		t.Fatal(err)
	}
	c, err := restarted.ClaimNext(ctx, sibling, time.Minute)
	if err != nil || c.ID != id || c.Attempt.Payload().Key.Number != 1 {
		t.Fatalf("independent claim: %v %v", c, err)
	}
	r := Reply{Outcome: Rejected, Echo: c.Attempt.Payload()}
	if got, err := restarted.AfterDispatch(ctx, c, r); got != Refused || err != nil {
		t.Fatalf("refusal: %v %v", got, err)
	}
	assertBusy(t, ctx, restarted, sibling)
}

func expire(t *testing.T, ctx context.Context, db *sql.DB, id int64) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `UPDATE gomar_fiscal_fixture.intents SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}
func confirmFixture(t *testing.T, p Pilot, o Operator, s Sale) Confirmed {
	t.Helper()
	d, err := Prepare(p, o, s)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Confirm(d, p, o, s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func assertBusy(t *testing.T, ctx context.Context, o *Outbox, p Pilot) {
	t.Helper()
	if _, err := o.ClaimNext(ctx, p, time.Minute); !errors.Is(err, ErrBusy) {
		t.Fatalf("held channel: %v", err)
	}
}
