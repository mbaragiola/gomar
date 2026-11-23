package tenantfiscalpreview

import "errors"

// FollowupScope identifies a saved check, not a table number. This fixture has
// no app caller and does not determine a real taxpayer's obligations.
type FollowupScope struct {
	Scope
	CheckID int64
}

type FollowupKind uint8

const (
	ServiceCompletedWitness FollowupKind = iota + 1
	ActualTenderWitness
	TenderWitnessCorrection
	SaleLink
)

var (
	ErrFollowupScope    = errors.New("synthetic follow-up unavailable")
	ErrFollowupEvent    = errors.New("invalid synthetic follow-up event")
	ErrFollowupConflict = errors.New("synthetic follow-up event conflicts with history")
)

// FollowupEvent represents an explicit synthetic witness, not a timestamped
// tender journal entry. The fixture neither captures nor verifies payment.
type FollowupEvent struct {
	Scope      FollowupScope
	ID         int64
	Kind       FollowupKind
	SaleID     int64
	CorrectsID int64
}

type FollowupStatus uint8

const (
	NoWitness FollowupStatus = iota
	ReviewPending
)

type FollowupView struct {
	Status FollowupStatus
	SaleID int64
}

type Followup struct {
	scope  FollowupScope
	events []FollowupEvent
	view   FollowupView
}

func NewFollowup(scope FollowupScope) (Followup, error) {
	if !scope.valid() {
		return Followup{}, ErrFollowupScope
	}
	return Followup{scope: scope}, nil
}

func (s FollowupScope) valid() bool {
	return s.EntityID > 0 && s.OutletID > 0 && s.CheckID > 0
}

func (f Followup) View(scope FollowupScope) (FollowupView, error) {
	if !scope.valid() || scope != f.scope {
		return FollowupView{}, ErrFollowupScope
	}
	return f.view, nil
}

// Record returns a new history snapshot. Events may arrive out of order:
// a sale link does not create a witness or dismiss a review; an explicit
// witness creates a pending review even when the sale was linked first.
func (f Followup) Record(e FollowupEvent) (Followup, error) {
	if !e.Scope.valid() || e.Scope != f.scope {
		return f, ErrFollowupScope
	}
	if e.ID < 1 {
		return f, ErrFollowupEvent
	}
	for _, old := range f.events {
		if old.ID == e.ID {
			if old == e {
				return f, nil
			}
			return f, ErrFollowupConflict
		}
	}
	view := f.view
	switch e.Kind {
	case ServiceCompletedWitness, ActualTenderWitness:
		if e.SaleID != 0 || e.CorrectsID != 0 {
			return f, ErrFollowupEvent
		}
		view.Status = ReviewPending
	case TenderWitnessCorrection:
		if e.SaleID != 0 || e.CorrectsID < 1 {
			return f, ErrFollowupEvent
		}
		var found bool
		for _, old := range f.events {
			if old.ID == e.CorrectsID && old.Kind == ActualTenderWitness {
				found = true
				break
			}
		}
		if !found {
			return f, ErrFollowupEvent
		}
		// Keep the original witness and pending review for an operator. This is
		// audit input, not verification or a decision that an invoice is due.
	case SaleLink:
		if e.SaleID < 1 || e.CorrectsID != 0 {
			return f, ErrFollowupEvent
		}
		if view.SaleID != 0 && view.SaleID != e.SaleID {
			return f, ErrFollowupConflict
		}
		view.SaleID = e.SaleID
	default:
		return f, ErrFollowupEvent
	}
	events := append(append([]FollowupEvent(nil), f.events...), e)
	return Followup{scope: f.scope, events: events, view: view}, nil
}
