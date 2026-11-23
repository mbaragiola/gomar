package fiscalcontroller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const testDeviceID = "counter-1"

func journalFixture(t *testing.T) (string, *Simulator, Config, *Controller) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "device.json")
	s := NewSimulator(7)
	cfg := Config{7, Capabilities{FiscalTicket: true}}
	c, err := CreateSimulatorJournal(path, testDeviceID, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return path, s, cfg, c
}

func resume(t *testing.T, path string, s *Simulator, cfg Config) *Controller {
	t.Helper()
	c, err := OpenSimulatorJournal(path, testDeviceID, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestJournalPersistsBeforeDeviceIOAndRecoversAfterLostResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		loss   NextIssue
		want   State
		issued int64
	}{
		{"before device issue", LoseResponseBeforeIssue, ConfirmedNotIssued, 0},
		{"after device issue", LoseResponseAfterIssue, Issued, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path, s, cfg, c := journalFixture(t)
			req := Request{"a", FiscalTicket, 5500}
			other := Request{"b", FiscalTicket, 6500}
			s.FailNextIssue(tc.loss)
			c.device = scripted{issue: func(ctx context.Context, r Request) (Document, error) {
				// Observe the durable state independently, inside the very first device
				// invocation so a journal save after I/O cannot make this test pass.
				saved, err := c.journal.load()
				if err != nil || saved.Pending != req.SaleID || saved.Records[req.SaleID] != (savedRecord{req, Result{State: OutcomeUnknown}}) {
					t.Fatalf("snapshot at device entry = %+v, %v", saved, err)
				}
				return s.Issue(ctx, r)
			}}
			if got, err := c.Issue(ctx, req); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
				t.Fatalf("first issue = %+v, %v", got, err)
			}
			c.Close()
			c = resume(t, path, s, cfg)
			if s.lastNumber != tc.issued {
				t.Fatalf("reopening issued %d documents, want %d", s.lastNumber, tc.issued)
			}
			for _, blocked := range []Request{req, other} {
				if _, err := c.Issue(ctx, blocked); !errors.Is(err, ErrNeedsReconciliation) {
					t.Fatalf("issue while unknown: %v", err)
				}
			}
			s.SetLookupInconclusive(true)
			if got, err := c.Reconcile(ctx, req.SaleID); got.State != OutcomeUnknown || !errors.Is(err, ErrNeedsReconciliation) {
				t.Fatalf("inconclusive after restart = %+v, %v", got, err)
			}
			s.SetLookupInconclusive(false)
			got, err := c.Reconcile(ctx, req.SaleID)
			if got.State != tc.want || err != nil || s.lastNumber != tc.issued {
				t.Fatalf("reconcile = %+v, %v; count=%d", got, err, s.lastNumber)
			}
			c.Close()
			// Both issued and confirmed-absent are persisted. Issue with the same
			// request is either a replay or the one explicit fresh submission.
			c = resume(t, path, s, cfg)
			if s.lastNumber != tc.issued {
				t.Fatal("reopen of settled journal touched device")
			}
			wantDoc := Document{req.SaleID, req.Kind, 7, 1, req.TotalCentavos}
			if got, err := c.Issue(ctx, req); got != (Result{Issued, wantDoc}) || err != nil || s.lastNumber != 1 {
				t.Fatalf("post-restart replay or explicit retry = %+v, %v; count=%d", got, err, s.lastNumber)
			}
			c.Close()
			c = resume(t, path, s, cfg)
			changed := req
			changed.TotalCentavos++
			if _, err := c.Issue(ctx, changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("modified replay = %v", err)
			}
			if got, err := c.Issue(ctx, other); got.State != Issued || got.Document.Number != 2 || err != nil {
				t.Fatalf("next sale = %+v, %v", got, err)
			}
		})
	}
}

func TestJournalUnavailablePreventsDeviceIO(t *testing.T) {
	ctx := context.Background()
	path, s, cfg, c := journalFixture(t)
	req := Request{"a", FiscalTicket, 100}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Issue(ctx, req); got.State != OutcomeUnknown || !errors.Is(err, ErrJournal) || s.lastNumber != 0 {
		t.Fatalf("submission without journal = %+v, %v; count=%d", got, err, s.lastNumber)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Issue(ctx, req); !errors.Is(err, ErrJournal) || s.lastNumber != 0 {
		t.Fatalf("unsafe retry on stale controller: %v; count=%d", err, s.lastNumber)
	}
	c.Close()
	c = resume(t, path, s, cfg)
	if _, err := c.Reconcile(ctx, req.SaleID); !errors.Is(err, ErrNoAttempt) {
		t.Fatalf("reconcile = %v", err)
	}
	if got, err := c.Issue(ctx, req); got.State != Issued || err != nil || s.lastNumber != 1 {
		t.Fatalf("fresh explicit issue = %+v, %v; count=%d", got, err, s.lastNumber)
	}
}

func TestJournalSaveFailureAfterDeviceIOKeepsUnknownAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path, s, cfg, c := journalFixture(t)
	req := Request{"a", FiscalTicket, 1100}
	c.device = scripted{issue: func(ctx context.Context, r Request) (Document, error) {
		got, err := s.Issue(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		// Remove access to the already-synced in-flight file before the controller
		// attempts to persist completion. Preserve that snapshot for recovery.
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		return got, nil
	}}
	if got, err := c.Issue(ctx, req); got != (Result{State: OutcomeUnknown}) || !errors.Is(err, ErrJournal) || s.lastNumber != 1 {
		t.Fatalf("failed final save = %+v, %v; count=%d", got, err, s.lastNumber)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reconcile(ctx, req.SaleID); !errors.Is(err, ErrJournal) {
		t.Fatalf("unsafe access through poisoned controller = %v", err)
	}
	c.Close()
	c = resume(t, path, s, cfg)
	if _, err := c.Issue(ctx, req); !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("unsafe resend after reopen = %v", err)
	}
	if got, err := c.Reconcile(ctx, req.SaleID); got.State != Issued || got.Document.Number != 1 || err != nil || s.lastNumber != 1 {
		t.Fatalf("reconcile = %+v, %v; count=%d", got, err, s.lastNumber)
	}
}

func TestJournalRejectsInvalidStorageOrScopeWithoutTouchingDevice(t *testing.T) {
	path, s, cfg, c := journalFixture(t)
	for _, retry := range []func() (*Controller, error){
		func() (*Controller, error) { return OpenSimulatorJournal(path, testDeviceID, s, cfg) },
		func() (*Controller, error) { return CreateSimulatorJournal(path, testDeviceID, s, cfg) },
	} {
		if dup, err := retry(); dup != nil || !errors.Is(err, ErrJournal) {
			t.Fatalf("opened live duplicate: %v, %v", dup, err)
		}
	}
	c.Close()
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if got, err := CreateSimulatorJournal(path, testDeviceID, s, cfg); got != nil || !errors.Is(err, ErrJournal) {
		t.Fatalf("recreated missing journal for bound simulator: %v, %v", got, err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		path   string
		id     string
		s      *Simulator
		cfg    Config
		create bool
	}{
		{"new same path", path, testDeviceID, s, cfg, true},
		{"new path for bound simulator", path + ".other", testDeviceID, s, cfg, true},
		{"missing", path + ".missing", testDeviceID, s, cfg, false},
		{"different named device", path, "counter-2", s, cfg, false},
		{"different simulator", path, testDeviceID, NewSimulator(7), cfg, false},
		{"changed capabilities", path, testDeviceID, s, Config{7, Capabilities{TicketInvoiceB: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := OpenSimulatorJournal
			if tc.create {
				attempt = CreateSimulatorJournal
			}
			if got, err := attempt(tc.path, tc.id, tc.s, tc.cfg); got != nil || !errors.Is(err, ErrJournal) {
				t.Fatalf("unexpected journal access: %v, %v", got, err)
			}
		})
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A valid JSON envelope with malformed payload or checksum is not an
	// empty journal. A failed Open must not repair or rewrite it.
	for _, bad := range []string{"", "{}", string(original) + "[]", string(original[:len(original)/2])} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if denied, err := OpenSimulatorJournal(path, testDeviceID, s, cfg); denied != nil || !errors.Is(err, ErrJournal) {
			t.Fatalf("invalid journal opened: %v, %v", denied, err)
		}
		if now, err := os.ReadFile(path); err != nil || string(now) != bad {
			t.Fatal("invalid journal was rewritten")
		}
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if denied, err := OpenSimulatorJournal(path, testDeviceID, s, cfg); denied != nil || !errors.Is(err, ErrJournal) {
		t.Fatalf("weak permissions accepted: %v, %v", denied, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	c = resume(t, path, s, cfg)
	if s.lastNumber != 0 {
		t.Fatal("journal operations issued a document")
	}
}
