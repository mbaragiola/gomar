// Package postender validates a manual ARS tender journal for one saved table
// check. It does not take, verify, or refund payments, create a sale, change
// stock, contact a fiscal device, or release the table.
package postender

import (
	"errors"
	"regexp"
)

const (
	Currency      = "ARS"
	MaxTotalMinor = int64(10_000_000_000) // Align with the POS cart limit.
	MaxLegs       = 100
)

var (
	ErrInvalid    = errors.New("invalid tender")
	ErrScope      = errors.New("tender scope does not match")
	ErrReplay     = errors.New("request was already used")
	ErrSealed     = errors.New("tender balance is sealed")
	ErrOverpay    = errors.New("tender exceeds remaining balance")
	ErrUnpaid     = errors.New("tender balance is not paid in full")
	ErrTooMany    = errors.New("too many active tender legs")
	ErrMissingLeg = errors.New("tender leg not found")
	ErrVoided     = errors.New("tender leg was already voided")
	requestIDRE   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// Scope identifies the immutable check, not the physical table number.
// Local table numbers can be reused later without sharing a journal.
type Scope struct {
	EntityID int64
	OutletID int64
	CheckID  int64
}

type Request struct {
	Scope     Scope
	ActorID   int64
	RequestID string
}

type Leg struct {
	Request     Request
	Method      string
	AmountMinor int64
}

// Void records a correction to a clerical entry, not a refund. TargetRequestID
// identifies an original Record in this journal. Reason is a fixed audit code;
// staff cannot place payment details or free-form notes in the journal.
type Void struct {
	Request         Request
	TargetRequestID string
	Reason          VoidReason
}

type VoidReason string

const (
	VoidEntryError  VoidReason = "entrada_incorrecta"
	VoidMethodError VoidReason = "medio_incorrecto"
	VoidAmountError VoidReason = "importe_incorrecto"
)

func (r VoidReason) Valid() bool {
	return r == VoidEntryError || r == VoidMethodError || r == VoidAmountError
}

// Balance is a snapshot. Paid in full is not sealed until the clerk issues
// an explicit Seal command. No result is proof of settled payment.
type Balance struct {
	TotalMinor       int64
	RecordedMinor    int64
	OutstandingMinor int64
	LegCount         int // Active records, excluding voided legs.
	Sealed           bool
}

type Journal struct {
	scope       Scope
	legs        []Leg
	voids       []Void
	records     map[string]Leg
	corrections map[string]Void
	voided      map[string]bool
	seal        *Request
	total       int64
	paid        int64
}

// New creates a disposable journal in memory, using the persisted check
// total. A future storage adapter must reconstruct/verify persisted state
// under the same check lock as the next write. It must authorize the actor
// for the branch and reject reusing a request ID on a different check within
// the branch before calling this aggregate. This type is not a permission
// check, a request registry shared across journals, or a concurrency lock.
func New(scope Scope, totalMinor int64) (*Journal, error) {
	if scope.EntityID < 1 || scope.OutletID < 1 || scope.CheckID < 1 ||
		totalMinor < 1 || totalMinor > MaxTotalMinor {
		return nil, ErrInvalid
	}
	return &Journal{scope: scope, total: totalMinor, records: make(map[string]Leg), corrections: make(map[string]Void), voided: make(map[string]bool)}, nil
}

// Record only appends a positive manual amount. An exact replay returns true
// without appending, even after seal; a changed actor, method, amount, or kind
// cannot reuse the ID. Repeated methods or amounts require distinct IDs.
func (j *Journal) Record(leg Leg) (replay bool, err error) {
	if err = j.validate(leg.Request); err != nil {
		return false, err
	}
	id := leg.Request.RequestID
	if _, used := j.corrections[id]; used || j.seal != nil && j.seal.RequestID == id {
		return false, ErrReplay
	}
	if existing, used := j.records[id]; used {
		if existing != leg {
			return false, ErrReplay
		}
		return true, nil
	}
	if j.seal != nil {
		return false, ErrSealed
	}
	if leg.AmountMinor <= 0 || !IsManualMethod(leg.Method) {
		return false, ErrInvalid
	}
	if leg.AmountMinor > j.total-j.paid {
		return false, ErrOverpay
	}
	// Leave room to finish the check rather than stranding a partial balance.
	active := len(j.legs) - len(j.voids)
	if active >= MaxLegs || (active == MaxLegs-1 && leg.AmountMinor != j.total-j.paid) {
		return false, ErrTooMany
	}
	j.legs = append(j.legs, leg)
	j.records[id] = leg
	j.paid += leg.AmountMinor
	return false, nil
}

// Seal freezes a full balance. It does not register a completed sale, and an
// attempt made while money remains outstanding changes nothing.
func (j *Journal) Seal(request Request) (replay bool, err error) {
	if err = j.validate(request); err != nil {
		return false, err
	}
	if j.seal != nil && j.seal.RequestID == request.RequestID {
		if *j.seal != request {
			return false, ErrReplay
		}
		return true, nil
	}
	if _, used := j.records[request.RequestID]; used {
		return false, ErrReplay
	}
	if _, used := j.corrections[request.RequestID]; used {
		return false, ErrReplay
	}
	if j.seal != nil {
		return false, ErrSealed
	}
	if j.paid != j.total {
		return false, ErrUnpaid
	}
	j.seal = &request
	return false, nil
}

// Void appends one pre-seal correction without changing the original record.
// The journal never invokes a payment service. Replaying an accepted original
// Record after a correction does not restore it.
func (j *Journal) Void(correction Void) (replay bool, err error) {
	if err = j.validate(correction.Request); err != nil {
		return false, err
	}
	id := correction.Request.RequestID
	if _, used := j.records[id]; used || j.seal != nil && j.seal.RequestID == id {
		return false, ErrReplay
	}
	if existing, used := j.corrections[id]; used {
		if existing != correction {
			return false, ErrReplay
		}
		return true, nil
	}
	if j.seal != nil {
		return false, ErrSealed
	}
	if id == correction.TargetRequestID || !requestIDRE.MatchString(correction.TargetRequestID) || !correction.Reason.Valid() {
		return false, ErrInvalid
	}
	leg, found := j.records[correction.TargetRequestID]
	if !found {
		return false, ErrMissingLeg
	}
	if j.voided[correction.TargetRequestID] {
		return false, ErrVoided
	}
	j.voids = append(j.voids, correction)
	j.corrections[id] = correction
	j.voided[correction.TargetRequestID] = true
	j.paid -= leg.AmountMinor
	return false, nil
}

func (j *Journal) validate(request Request) error {
	if request.Scope != j.scope {
		return ErrScope
	}
	if j.total < 1 || request.ActorID < 1 || !requestIDRE.MatchString(request.RequestID) {
		return ErrInvalid
	}
	return nil
}

func (j *Journal) Balance() Balance {
	return Balance{j.total, j.paid, j.total - j.paid, len(j.legs) - len(j.voids), j.seal != nil}
}

// Legs returns all original records, even corrected ones, in a detached slice.
func (j *Journal) Legs() []Leg {
	return append([]Leg(nil), j.legs...)
}

// Voids returns the append-only corrections in recording order in a detached
// slice. A durable adapter should assign cross-kind order and timestamps.
func (j *Journal) Voids() []Void {
	return append([]Void(nil), j.voids...)
}

// IsManualMethod matches the current one-method POS names. These labels
// describe how the clerk handled a payment elsewhere, not an instruction to
// any provider.
func IsManualMethod(method string) bool {
	switch method {
	case "debito", "credito", "qr_modo", "qr_mercado_pago", "transferencia", "efectivo":
		return true
	default:
		return false
	}
}
