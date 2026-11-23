package tenantfiscalpreview

import (
	"errors"
	"reflect"
	"testing"
)

var followupScope = FollowupScope{Scope: Scope{EntityID: 1, OutletID: 2}, CheckID: 3}

func event(id int64, kind FollowupKind) FollowupEvent {
	return FollowupEvent{Scope: followupScope, ID: id, Kind: kind}
}

func record(t *testing.T, f Followup, e FollowupEvent) Followup {
	t.Helper()
	next, err := f.Record(e)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func newFollowup(t *testing.T) Followup {
	t.Helper()
	f, err := NewFollowup(followupScope)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func assertFollowup(t *testing.T, f Followup, want FollowupView) {
	t.Helper()
	view, err := f.View(followupScope)
	if view != want || err != nil {
		t.Fatalf("got (%+v,%v), want %+v", view, err, want)
	}
}

func TestFollowupKeepsEarlyWitnessThroughCorrectionAndSale(t *testing.T) {
	blank := newFollowup(t)
	witness := event(2, ActualTenderWitness)
	pending := record(t, blank, witness)
	assertFollowup(t, pending, FollowupView{Status: ReviewPending})
	correction := event(3, TenderWitnessCorrection)
	correction.CorrectsID = 2
	corrected := record(t, pending, correction)
	assertFollowup(t, corrected, FollowupView{Status: ReviewPending})
	sale := event(4, SaleLink)
	sale.SaleID = 10
	linked := record(t, corrected, sale)
	assertFollowup(t, linked, FollowupView{Status: ReviewPending, SaleID: 10})
	for _, e := range []FollowupEvent{witness, correction, sale} {
		if replay := record(t, linked, e); !reflect.DeepEqual(replay, linked) {
			t.Fatal("exact replay mutated history")
		}
	}
	assertFollowup(t, blank, FollowupView{Status: NoWitness})
	assertFollowup(t, pending, FollowupView{Status: ReviewPending})
	if len(blank.events) != 0 || len(pending.events) != 1 || len(linked.events) != 3 {
		t.Fatal("earlier snapshot mutated")
	}
}

func TestFollowupSaleLinkAndOutOfOrderCompletion(t *testing.T) {
	start := newFollowup(t)
	sale := event(10, SaleLink)
	sale.SaleID = 40
	linked := record(t, start, sale)
	assertFollowup(t, linked, FollowupView{Status: NoWitness, SaleID: 40})
	completion := event(1, ServiceCompletedWitness)
	finished := record(t, linked, completion)
	assertFollowup(t, finished, FollowupView{Status: ReviewPending, SaleID: 40})
	completionFirst := record(t, record(t, start, completion), sale)
	assertFollowup(t, completionFirst, FollowupView{Status: ReviewPending, SaleID: 40})
}

func TestFollowupCannotReadOrMutateAnotherCheckOrOutlet(t *testing.T) {
	f := record(t, newFollowup(t), event(1, ServiceCompletedWitness))
	scopes := []FollowupScope{{Scope: Scope{EntityID: 2, OutletID: 2}, CheckID: 3}, {Scope: Scope{EntityID: 1, OutletID: 7}, CheckID: 3}, {Scope: followupScope.Scope, CheckID: 4}}
	for _, scope := range scopes {
		if view, err := f.View(scope); view != (FollowupView{}) || !errors.Is(err, ErrFollowupScope) {
			t.Fatalf("foreign view: %+v %v", view, err)
		}
		e := event(2, ActualTenderWitness)
		e.Scope = scope
		if after, err := f.Record(e); !reflect.DeepEqual(after, f) || !errors.Is(err, ErrFollowupScope) {
			t.Fatalf("foreign update: %v", err)
		}
	}
	if replacement, err := NewFollowup(scopes[2]); err != nil {
		t.Fatal(err)
	} else if view, err := replacement.View(scopes[2]); view.Status != NoWitness || err != nil {
		t.Fatalf("replacement check inherited state: %+v %v", view, err)
	}
}

func TestFollowupRejectsInventedOrConflictingFacts(t *testing.T) {
	f := record(t, newFollowup(t), event(1, ServiceCompletedWitness))
	sale := event(2, SaleLink)
	sale.SaleID = 3
	f = record(t, f, sale)
	changed := sale
	changed.SaleID = 8
	replacement := changed
	replacement.ID = 3
	for _, e := range []FollowupEvent{changed, replacement} {
		if after, err := f.Record(e); !reflect.DeepEqual(after, f) || !errors.Is(err, ErrFollowupConflict) {
			t.Fatalf("conflict: %v", err)
		}
	}
	wrongCorrection := event(4, TenderWitnessCorrection)
	wrongCorrection.CorrectsID = 1
	for _, e := range []FollowupEvent{event(5, FollowupKind(5)), event(0, ActualTenderWitness), event(5, SaleLink), wrongCorrection} {
		if after, err := f.Record(e); !reflect.DeepEqual(after, f) || !errors.Is(err, ErrFollowupEvent) {
			t.Fatalf("invalid: %v", err)
		}
	}
}
