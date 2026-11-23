// Package tenantfiscalpreview models a synthetic, outlet-scoped fiscal invoice
// decision. It has no network client, account, keys, or production entry point.
package tenantfiscalpreview

import (
	"errors"
	"regexp"
	"time"
)

var (
	ErrUnavailable = errors.New("fiscal preview unavailable")
	ErrSale        = errors.New("unsupported fiscal sale fixture")
	ErrChanged     = errors.New("fiscal preview changed; review it again")
	ErrAttempt     = errors.New("invalid fiscal attempt")
)

var fixtureIssuer = regexp.MustCompile(`^fixture-[a-z][a-z0-9-]{0,39}$`)
var authorizationCode = regexp.MustCompile(`^[0-9]{14}$`)

const (
	// The transport-free fixture supports one ordinary ARS invoice C to an
	// unidentified final consumer. This is not a choice of class or threshold
	// for any real business.
	invoiceC        = 11
	currencyARS     = "PES"
	finalConsumer   = 5
	maxFixtureTotal = 100_000_00
	maxPointOfSale  = 99999
	maxNumber       = 99999999
)

type Scope struct {
	EntityID, OutletID int64
}

// Pilot is resolved by trusted server configuration, never by an operator or chat.
// The issuer is a fixture-only alias; it cannot contain a real tax ID.
type Pilot struct {
	Enabled     bool
	Scope       Scope
	Issuer      string
	PointOfSale int
}

// Operator is derived from the current authenticated account and outlet grant.
// Issue permission is distinct from being able to record a sale or payment.
type Operator struct {
	Scope    Scope
	ActorID  int64
	MayIssue bool
}

// Sale is a trusted server snapshot, not a request body. A local or unsynced
// cart is not eligible. Due and OperationDate describe the sale, not the tender;
// in particular the date is not when an offline record finally synced.
type Sale struct {
	Scope          Scope
	ID             int64
	TotalMinor     int64
	OperationDate  string
	OnlineRecorded bool
	Due            bool
}

type Preview struct {
	pilot    Pilot
	operator Operator
	sale     Sale
}

func Prepare(pilot Pilot, operator Operator, sale Sale) (Preview, error) {
	if !pilot.Enabled || pilot.Scope.EntityID < 1 || pilot.Scope.OutletID < 1 ||
		!fixtureIssuer.MatchString(pilot.Issuer) || pilot.PointOfSale < 1 || pilot.PointOfSale > maxPointOfSale ||
		!operator.MayIssue || operator.ActorID < 1 || operator.Scope != pilot.Scope || sale.Scope != pilot.Scope {
		return Preview{}, ErrUnavailable
	}
	if sale.ID < 1 || !sale.Due || !sale.OnlineRecorded ||
		sale.TotalMinor < 1 || sale.TotalMinor > maxFixtureTotal || !civilDate(sale.OperationDate) {
		return Preview{}, ErrSale
	}
	return Preview{pilot: pilot, operator: operator, sale: sale}, nil
}

// Confirm requires the exact reviewed preview and current server facts, for
// the actor who prepared it. Restrict the eventual caller to native human
// confirmation; the function itself cannot attest how the caller authenticated.
func Confirm(reviewed Preview, pilot Pilot, operator Operator, sale Sale) (Confirmed, error) {
	current, err := Prepare(pilot, operator, sale)
	if err != nil {
		return Confirmed{}, err
	}
	if reviewed != current {
		return Confirmed{}, ErrChanged
	}
	return Confirmed{preview: current}, nil
}

type Confirmed struct{ preview Preview }

// Key identifies one provider document number inside this synthetic mode.
// Future repository code must reserve the number durably per mode, issuer,
// point of sale, and document type, rather than per browser or sale.
type Key struct {
	Issuer                            string
	PointOfSale, DocumentType, Number int
}

// Payload contains the fields echoed by the synthetic provider. An eventual
// real adapter must normalize, compare and validate every relevant ARCA field;
// it cannot treat these reduced fixture fields as the full wire schema.
type Payload struct {
	Key                 Key
	IssueDate, Currency string
	RecipientKind       int
	TotalMinor          int64
}

type Attempt struct {
	confirmed Confirmed
	payload   Payload
}

// WithReservedNumber binds a number supplied by the caller. It does not
// allocate, store, submit or retry an attempt.
func (c Confirmed) WithReservedNumber(number int) (Attempt, error) {
	if !c.preview.valid() || number < 1 || number > maxNumber {
		return Attempt{}, ErrAttempt
	}
	p := Payload{Key: Key{Issuer: c.preview.pilot.Issuer, PointOfSale: c.preview.pilot.PointOfSale, DocumentType: invoiceC, Number: number},
		IssueDate: c.preview.sale.OperationDate, Currency: currencyARS, RecipientKind: finalConsumer, TotalMinor: c.preview.sale.TotalMinor}
	return Attempt{confirmed: c, payload: p}, nil
}

func (p Preview) valid() bool {
	current, err := Prepare(p.pilot, p.operator, p.sale)
	return err == nil && current == p
}

func (a Attempt) Payload() Payload { return a.payload }

type Outcome uint8

const (
	NoOutcome Outcome = iota
	Approved
	Rejected
	NotFound
	Unavailable
)

type Reply struct {
	Outcome         Outcome
	Echo            Payload
	Code, ExpiresOn string
}

type Status uint8

const (
	NoStatus Status = iota
	Unknown
	Authorized
	Refused
	NeedsReview
)

type Decision struct {
	attempt         Attempt
	status          Status
	code, expiresOn string
}

func (d Decision) Status() Status { return d.status }

// Authorization returns an authorization only for a validated decision.
func (d Decision) Authorization() (code, expiresOn string, ok bool) {
	return d.code, d.expiresOn, d.status == Authorized
}

// AfterSubmit applies an observation from one dispatch, not a retry. Transport
// errors, absent replies and malformed approvals are uncertain.
func AfterSubmit(attempt Attempt, reply Reply) (Decision, error) {
	exact, err := attempt.confirmed.WithReservedNumber(attempt.payload.Key.Number)
	if err != nil || exact != attempt {
		return Decision{}, ErrAttempt
	}
	switch {
	case approved(attempt, reply):
		return Decision{attempt: attempt, status: Authorized, code: reply.Code, expiresOn: reply.ExpiresOn}, nil
	case reply.Outcome == Rejected && reply.Echo == attempt.payload && reply.Code == "" && reply.ExpiresOn == "":
		return Decision{attempt: attempt, status: Refused}, nil
	default:
		return Decision{attempt: attempt, status: Unknown}, nil
	}
}

// AfterLookup accepts only an exact approval of this same uncertain attempt.
// A transport error or not-found result alone does not authorize a retry or
// number advance. A discordant response requires manual review.
func AfterLookup(prior Decision, attempt Attempt, reply Reply) (Decision, error) {
	if prior.status != Unknown || prior.attempt != attempt {
		return Decision{}, ErrAttempt
	}
	if approved(attempt, reply) {
		return Decision{attempt: attempt, status: Authorized, code: reply.Code, expiresOn: reply.ExpiresOn}, nil
	}
	if (reply.Outcome == Unavailable || reply.Outcome == NotFound) && reply.Echo == (Payload{}) && reply.Code == "" && reply.ExpiresOn == "" {
		return prior, nil
	}
	return Decision{attempt: attempt, status: NeedsReview}, nil
}

func approved(attempt Attempt, reply Reply) bool {
	return reply.Outcome == Approved && reply.Echo == attempt.payload && authorizationCode.MatchString(reply.Code) &&
		reply.Code != "00000000000000" && civilDate(reply.ExpiresOn) && reply.ExpiresOn >= attempt.payload.IssueDate
}

func civilDate(value string) bool {
	if len(value) != len(time.DateOnly) {
		return false
	}
	parsed, err := time.Parse(time.DateOnly, value)
	return err == nil && parsed.Year() >= 2000
}
