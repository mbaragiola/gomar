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
	ErrInvalid  = errors.New("invalid tender")
	ErrScope    = errors.New("tender scope does not match")
	ErrReplay   = errors.New("request was already used")
	ErrSealed   = errors.New("tender balance is sealed")
	ErrOverpay  = errors.New("tender exceeds remaining balance")
	ErrUnpaid   = errors.New("tender balance is not paid in full")
	ErrTooMany  = errors.New("too many tender legs")
	requestIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
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

// Balance is a snapshot. Paid in full is not sealed until the clerk issues
// an explicit Seal command. No result is proof of settled payment.
type Balance struct {
	TotalMinor       int64
	RecordedMinor    int64
	OutstandingMinor int64
	LegCount         int
	Sealed           bool
}

type Journal struct {
	scope Scope
	legs  []Leg
	seal  *Request
	total int64
	paid  int64
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
	return &Journal{scope: scope, total: totalMinor}, nil
}

// Record only appends a positive manual amount. An exact replay returns true
// without appending, even after seal; a changed actor, method, amount, or kind
// cannot reuse the ID. Repeated methods or amounts require distinct IDs.
func (j *Journal) Record(leg Leg) (replay bool, err error) {
	if err = j.validate(leg.Request); err != nil {
		return false, err
	}
	if j.seal != nil && j.seal.RequestID == leg.Request.RequestID {
		return false, ErrReplay
	}
	for _, existing := range j.legs {
		if existing.Request.RequestID == leg.Request.RequestID {
			if existing != leg {
				return false, ErrReplay
			}
			return true, nil
		}
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
	if len(j.legs) >= MaxLegs || (len(j.legs) == MaxLegs-1 && leg.AmountMinor != j.total-j.paid) {
		return false, ErrTooMany
	}
	j.legs = append(j.legs, leg)
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
	for _, leg := range j.legs {
		if leg.Request.RequestID == request.RequestID {
			return false, ErrReplay
		}
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
	return Balance{j.total, j.paid, j.total - j.paid, len(j.legs), j.seal != nil}
}

// Legs returns a detached copy. A caller cannot edit or remove journal entries.
func (j *Journal) Legs() []Leg {
	return append([]Leg(nil), j.legs...)
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
