package main

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const eligibilityFixtureIssuer = "eligibility-source-qa"
const eligibilityFixtureAudience = "gomar-hosted-eligibility-qa"

var errEligibilityFixtureSource = errors.New("synthetic source unavailable")

type eligibilityFixtureSource struct {
	mu           sync.Mutex
	observations map[string]hostedEligibilityObservation
	onVerify     func(hostedEligibilityBinding)
}

func newEligibilityFixture(now func() time.Time) (*hostedDiscoveryReadFixture, *eligibilityFixtureSource) {
	s := &eligibilityFixtureSource{observations: make(map[string]hostedEligibilityObservation)}
	f := &hostedDiscoveryReadFixture{now: now, observationSource: &hostedEligibilitySource{eligibilityFixtureIssuer, eligibilityFixtureAudience, s.verify}}
	return f, s
}

func (s *eligibilityFixtureSource) publish(key string, o hostedEligibilityObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o.binding.branches = slices.Clone(o.binding.branches)
	s.observations[key] = o
}

func (s *eligibilityFixtureSource) verify(_ context.Context, b hostedEligibilityBinding) (hostedEligibilityObservation, error) {
	s.mu.Lock()
	o, ok := s.observations[b.business]
	after := s.onVerify
	s.mu.Unlock()
	if after != nil {
		after(b)
	}
	if !ok {
		return hostedEligibilityObservation{}, errEligibilityFixtureSource
	}
	return o, nil
}

func fixtureHostedEligibility(business string, branches []string, epoch int64, state string, at, until time.Time) hostedEligibilityObservation {
	return hostedEligibilityObservation{hostedEligibilityBinding{eligibilityFixtureIssuer, eligibilityFixtureAudience, hostedEligibilityPurpose, business, branches}, epoch, state, at, until}
}

func (s *eligibilityFixtureSource) apply(t *testing.T, ctx context.Context, a *app, o hostedEligibilityObservation, want hostedEligibilityApplyResult) {
	t.Helper()
	s.publish(o.binding.business, o)
	if r, err := a.applyHostedEligibilityObservation(ctx, o.binding.business, o.binding.branches); err != nil || r != want {
		t.Fatalf("fixture did not apply eligibility: result %d, want %d: %v", r, want, err)
	}
}

func TestPostgresHostedEligibilityApply(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const paid = "paid-alpha"
	const incomplete = "paid-incomplete"
	branches := []string{"A-a", "A-b"}
	start := time.Now().UTC().Truncate(time.Microsecond)
	var clock atomic.Int64
	at := func(offset time.Duration) time.Time { return start.Add(offset) }
	move := func(offset time.Duration) { clock.Store(at(offset).UnixMicro()) }
	move(0)
	fixture, source := newEligibilityFixture(func() time.Time { return time.UnixMicro(clock.Load()) })
	a := &app{db: db, hostedDiscoveryRead: fixture}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.users(id,username,password,active,staff,superuser)
		VALUES(5001,'synthetic-eligibility-user','fixture',TRUE,FALSE,FALSE);
		INSERT INTO gomar.entities(id,name,cuit,active) VALUES(5101,'QA paid','00000005101',TRUE);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES(5111,5101,'QA A',TRUE),(5112,5101,'QA B',TRUE),(5113,5101,'QA incomplete',TRUE);
		INSERT INTO gomar.memberships(id,user_id,entity_id,role,active) VALUES(5101,5001,5101,'read_only',TRUE);
		INSERT INTO gomar.outlet_grants VALUES(5101,5111),(5101,5112);
		INSERT INTO gomar.discovery_entity_class VALUES(5101,'paid_hosted');
		INSERT INTO gomar.discovery_paid_scope VALUES('paid-alpha',ARRAY['A-a','A-b']),('paid-incomplete',ARRAY['B-a','B-b']);
		INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref) VALUES
		(5111,5101,'paid-alpha','A-a'),(5112,5101,'paid-alpha','A-b'),(5113,5101,'paid-incomplete','B-a')`)
	count := func() (n int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM gomar.discovery_scope_epoch`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	discover := func(want bool) {
		t.Helper()
		allow, err := fixture.discover(ctx, db, 5001, sql.NullInt64{Int64: 5101, Valid: true}, sql.NullInt64{Int64: 5111, Valid: true}, false)
		if err != nil || (len(allow[5101]) == 1) != want {
			t.Fatalf("scoped reader admission=%v, want %v: %v", len(allow[5101]) == 1, want, err)
		}
	}
	observation := func(epoch int64, state string, checked, deadline time.Duration) hostedEligibilityObservation {
		return fixtureHostedEligibility(paid, branches, epoch, state, at(checked), at(deadline))
	}
	positive := observation(1, "eligible", -time.Second, 4*time.Second)
	deny := func(app *app, business string, target []string) {
		t.Helper()
		n := count()
		if r, err := app.applyHostedEligibilityObservation(ctx, business, target); r != hostedEligibilityNotRecorded || !errors.Is(err, hostedEligibilityUnavailable) || count() != n {
			t.Fatalf("denied observation was applied: %d %v", r, err)
		}
	}
	deny(a, paid, branches) // Verifier failed: no existing lease and no state change.
	source.publish(paid, positive)
	deny(&app{db: db}, paid, branches) // No opt-in, even with a healthy source elsewhere.
	deny(&app{db: db, hostedDiscoveryRead: &hostedDiscoveryReadFixture{}}, paid, branches)
	for _, alter := range []func(*hostedEligibilityObservation){
		func(o *hostedEligibilityObservation) { o.binding.issuer = "imposter" },
		func(o *hostedEligibilityObservation) { o.binding.audience = "foreign" },
		func(o *hostedEligibilityObservation) { o.binding.purpose = nativeInstallPurpose },
		func(o *hostedEligibilityObservation) { o.binding.business = "other-business" },
		func(o *hostedEligibilityObservation) { o.binding.branches = []string{"A-a"} },
		func(o *hostedEligibilityObservation) { o.binding.branches = []string{"A-b", "A-a"} },
		func(o *hostedEligibilityObservation) { o.binding.branches = []string{"A-a", "A-a"} },
		func(o *hostedEligibilityObservation) { o.state = "paid" },
		func(o *hostedEligibilityObservation) { o.epoch = 0 },
		func(o *hostedEligibilityObservation) { o.validUntil = o.checkedAt.Add(6 * time.Second) },
		func(o *hostedEligibilityObservation) { o.validUntil = o.checkedAt },
		func(o *hostedEligibilityObservation) { o.checkedAt = o.checkedAt.Add(time.Nanosecond) },
		func(o *hostedEligibilityObservation) { o.checkedAt = at(time.Second) },
	} {
		o := positive
		alter(&o)
		source.publish(paid, o)
		deny(a, paid, branches)
	}
	deny(a, paid, []string{"A-b", "A-a"})
	source.publish(incomplete, fixtureHostedEligibility(incomplete, []string{"B-a", "B-b"}, 1, "eligible", at(-time.Second), at(time.Second)))
	deny(a, incomplete, []string{"B-a", "B-b"}) // Declared scope exists but only one branch is mapped.
	source.publish(paid, fixtureHostedEligibility(paid, []string{"A-a"}, 1, "eligible", at(-time.Second), at(time.Second)))
	deny(a, paid, []string{"A-a"}) // Exact verifier response still cannot redefine a local scope.
	if count() != 0 {
		t.Fatal("denials left evidence")
	}
	source.apply(t, ctx, a, positive, hostedEligibilityRecorded)
	discover(true)
	source.apply(t, ctx, a, positive, hostedEligibilityReplay)
	move(4100 * time.Millisecond)
	discover(false)
	source.apply(t, ctx, a, positive, hostedEligibilityReplay) // Exact history cannot refresh expiry.
	discover(false)
	if count() != 1 {
		t.Fatal("positive replay wrote a second row")
	}

	fresh := observation(4, "eligible", 4*time.Second, 8*time.Second)
	source.apply(t, ctx, a, fresh, hostedEligibilityRecorded)
	source.apply(t, ctx, a, observation(2, "eligible", 4*time.Second, 8*time.Second), hostedEligibilityOlderPositive)
	source.apply(t, ctx, a, observation(2, "suspended", 3950*time.Millisecond, 4050*time.Millisecond), hostedEligibilityRecorded)
	discover(true)
	move(4600 * time.Millisecond)
	// Arrival after the source's expiry still preserves a *verified adverse*
	// fact, whose later checked-at conflicts with the winning older check.
	late := observation(3, "suspended", 4200*time.Millisecond, 4300*time.Millisecond)
	source.apply(t, ctx, a, late, hostedEligibilityRecorded)
	source.apply(t, ctx, a, late, hostedEligibilityReplay)
	discover(false)
	source.publish(paid, observation(5, "eligible", 4100*time.Millisecond, 8*time.Second))
	deny(a, paid, branches) // Newer epoch, stale chronology.
	source.apply(t, ctx, a, observation(5, "eligible", 4500*time.Millisecond, 8*time.Second), hostedEligibilityRecorded)
	discover(true)
	conflict := observation(5, "unknown", 4450*time.Millisecond, 4500*time.Millisecond)
	source.apply(t, ctx, a, conflict, hostedEligibilityDisputed) // Same epoch, expired independent disagreement.
	source.apply(t, ctx, a, conflict, hostedEligibilityReplay)
	discover(false)
	source.apply(t, ctx, a, observation(6, "eligible", 4550*time.Millisecond, 8*time.Second), hostedEligibilityRecorded)
	discover(true)
	// A novel observation at a previously seen historical epoch creates a
	// durable dispute, even if both claims assert eligible at distinct times.
	source.apply(t, ctx, a, observation(4, "eligible", 4600*time.Millisecond, 8*time.Second), hostedEligibilityDisputed)
	discover(false)
	move(5 * time.Second)
	source.apply(t, ctx, a, observation(7, "eligible", 4800*time.Millisecond, 9*time.Second), hostedEligibilityRecorded)
	discover(true)
	source.mu.Lock()
	delete(source.observations, paid)
	source.mu.Unlock()
	deny(a, paid, branches)
	discover(true) // An unavailable source cannot renew or undo an existing bounded lease.
	move(9 * time.Second)
	discover(false)

	t.Run("verifies_before_lock_and_uses_postlock_time", func(t *testing.T) {
		move(10 * time.Second)
		candidate := observation(8, "eligible", 10*time.Second, 11*time.Second)
		source.publish(paid, candidate)
		lock, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback()
		if _, err = lock.ExecContext(ctx, `SELECT business_ref FROM gomar.discovery_paid_scope WHERE business_ref=$1 FOR UPDATE`, paid); err != nil {
			t.Fatal(err)
		}
		verified := make(chan struct{}, 1)
		source.mu.Lock()
		source.onVerify = func(hostedEligibilityBinding) { verified <- struct{}{} }
		source.mu.Unlock()
		defer func() { source.mu.Lock(); source.onVerify = nil; source.mu.Unlock() }()
		type outcome struct {
			r   hostedEligibilityApplyResult
			err error
		}
		done := make(chan outcome, 1)
		go func() { r, e := a.applyHostedEligibilityObservation(ctx, paid, branches); done <- outcome{r, e} }()
		select {
		case <-verified:
		case <-ctx.Done():
			t.Fatal("verification ran after the already-held scope lock")
		}
		waitUntilHostedObservationLock(t, ctx, db, 1)
		move(11200 * time.Millisecond)
		if err = lock.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-done:
			if got.r != hostedEligibilityNotRecorded || !errors.Is(got.err, hostedEligibilityUnavailable) {
				t.Fatalf("pre-lock time escaped: %+v", got)
			}
		case <-ctx.Done():
			t.Fatal("lock waiter did not finish")
		}
		var reused bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gomar.discovery_scope_epoch WHERE business_ref=$1 AND epoch=8)`, paid).Scan(&reused); err != nil || reused {
			t.Fatal("expired lock waiter wrote an epoch")
		}
	})
	t.Run("overlap_authentic_same_epoch_disputes", func(t *testing.T) {
		move(12 * time.Second)
		lock, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback()
		if _, err = lock.ExecContext(ctx, `SELECT business_ref FROM gomar.discovery_paid_scope WHERE business_ref=$1 FOR UPDATE`, paid); err != nil {
			t.Fatal(err)
		}
		verified := make(chan struct{}, 2)
		source.mu.Lock()
		source.onVerify = func(hostedEligibilityBinding) { verified <- struct{}{} }
		source.mu.Unlock()
		defer func() { source.mu.Lock(); source.onVerify = nil; source.mu.Unlock() }()
		type outcome struct {
			r   hostedEligibilityApplyResult
			err error
		}
		done := make(chan outcome, 2)
		startApply := func(o hostedEligibilityObservation) {
			t.Helper()
			source.publish(paid, o)
			go func() { r, e := a.applyHostedEligibilityObservation(ctx, paid, branches); done <- outcome{r, e} }()
			select {
			case <-verified:
			case <-ctx.Done():
				t.Fatal("source was not called before lock")
			}
		}
		one := observation(9, "eligible", 11400*time.Millisecond, 15*time.Second)
		two := observation(9, "suspended", 11600*time.Millisecond, 15*time.Second)
		startApply(one)
		waitUntilHostedObservationLock(t, ctx, db, 1)
		startApply(two)
		waitUntilHostedObservationLock(t, ctx, db, 2)
		if err = lock.Commit(); err != nil {
			t.Fatal(err)
		}
		counts := map[hostedEligibilityApplyResult]int{}
		for range 2 {
			select {
			case got := <-done:
				if got.err != nil {
					t.Fatalf("overlapping observation: %v", got.err)
				}
				counts[got.r]++
			case <-ctx.Done():
				t.Fatal("overlapping writer did not finish")
			}
		}
		if counts[hostedEligibilityRecorded] != 1 || counts[hostedEligibilityDisputed] != 1 {
			t.Fatalf("overlap did not durably dispute: %v", counts)
		}
		discover(false)
	})

}

func waitUntilHostedObservationLock(t *testing.T, ctx context.Context, db *sql.DB, minimum int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	wait, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		var blocked bool
		if err := db.QueryRowContext(wait, `SELECT COUNT(*)>=$1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock'
			AND query LIKE 'SELECT branches=$2::text[] FROM gomar.discovery_paid_scope%'`, minimum).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-wait.Done():
			t.Fatal("observation never waited on the actual PG scope lock")
		case <-ticker.C:
		}
	}
}
