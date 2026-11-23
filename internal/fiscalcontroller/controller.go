// Package fiscalcontroller models explicit issuance on one fiscal controller.
// It is simulator-only until a durable journal and a validated adapter exist.
package fiscalcontroller

import (
	"context"
	"errors"
	"fmt"
)

type Kind string

const (
	FiscalTicket   Kind = "fiscal_ticket"
	TicketInvoiceA Kind = "ticket_invoice_a"
	TicketInvoiceB Kind = "ticket_invoice_b"
	TicketInvoiceC Kind = "ticket_invoice_c"
)

type Capabilities struct {
	FiscalTicket, TicketInvoiceA, TicketInvoiceB, TicketInvoiceC bool
}

func (c Capabilities) supports(kind Kind) bool {
	switch kind {
	case FiscalTicket:
		return c.FiscalTicket
	case TicketInvoiceA:
		return c.TicketInvoiceA
	case TicketInvoiceB:
		return c.TicketInvoiceB
	case TicketInvoiceC:
		return c.TicketInvoiceC
	default:
		return false
	}
}

type Config struct {
	PointOfSale  int
	Capabilities Capabilities
}

// Request is an explicit issue instruction, not a sale payment or table check.
// SaleID is the stable identity of a sale within this device's scope. The
// simulator envelope is not enough to supply the fields of a real fiscal sale.
type Request struct {
	SaleID        string
	Kind          Kind
	TotalCentavos int64
}

// Document is evidence of what the device issued. The adapter must verify
// these fields against the actual issuance, not simply echo the request.
type Document struct {
	SaleID        string
	Kind          Kind
	PointOfSale   int
	Number        int64
	TotalCentavos int64
}

type LookupState uint8

const (
	Inconclusive LookupState = iota
	Found
	// NotIssued requires authoritative evidence for this exact request. A
	// timeout, missing paper, or inability to correlate a document is not proof.
	NotIssued
)

type LookupResult struct {
	State    LookupState
	Document Document
}

// Device is the transport boundary. Any Issue error leaves the outcome
// unknown. Lookup must not issue, reprint, or change a fiscal document.
type Device interface {
	Issue(context.Context, Request) (Document, error)
	Lookup(context.Context, Request) (LookupResult, error)
}

type State uint8

const (
	NoAttempt State = iota
	OutcomeUnknown
	Issued
	ConfirmedNotIssued
)

type Result struct {
	State    State
	Document Document
}

var (
	ErrInvalid             = errors.New("invalid fiscal controller request")
	ErrUnsupported         = errors.New("fiscal document kind not supported")
	ErrConflict            = errors.New("fiscal request differs from the recorded sale")
	ErrNeedsReconciliation = errors.New("fiscal controller has an unresolved issue")
	ErrNoAttempt           = errors.New("no fiscal issue attempt for this sale")
)

type documentNumber struct {
	kind   Kind
	number int64
}

type record struct {
	request Request
	result  Result
}

// Controller serializes submissions and lookups for one device. Create only
// one Controller per device. Its in-memory state survives neither a process
// restart nor use by multiple workers; do not use this with real hardware.
type Controller struct {
	device        Device
	config        Config
	gate          chan struct{}
	records       map[string]record
	pending       string
	issuedNumbers map[documentNumber]string
}

func New(device Device, cfg Config) (*Controller, error) {
	if device == nil || cfg.PointOfSale < 1 || cfg.PointOfSale > 99999 || cfg.Capabilities == (Capabilities{}) {
		return nil, ErrInvalid
	}
	return &Controller{
		device: device, config: cfg, gate: make(chan struct{}, 1),
		records: make(map[string]record), issuedNumbers: make(map[documentNumber]string),
	}, nil
}

func (c *Controller) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			c.unlock()
			return err
		}
		return nil
	}
}
func (c *Controller) unlock() { <-c.gate }

func (c *Controller) valid(req Request, doc Document) bool {
	existingSale, used := c.issuedNumbers[documentNumber{doc.Kind, doc.Number}]
	return doc.SaleID == req.SaleID && doc.Kind == req.Kind && doc.TotalCentavos == req.TotalCentavos && doc.PointOfSale == c.config.PointOfSale && doc.Number > 0 && (!used || existingSale == req.SaleID)
}

// Issue submits once. A replay of a completed request returns its existing
// document. An unresolved attempt blocks further sends to this device. The
// caller may explicitly retry the same request only after a conclusive lookup.
func (c *Controller) Issue(ctx context.Context, req Request) (Result, error) {
	if req.SaleID == "" || req.TotalCentavos <= 0 {
		return Result{}, ErrInvalid
	}
	if !c.config.Capabilities.supports(req.Kind) {
		return Result{}, ErrUnsupported
	}
	if err := c.lock(ctx); err != nil {
		return Result{}, err
	}
	defer c.unlock()
	rec, exists := c.records[req.SaleID]
	if exists && rec.request != req {
		return Result{}, ErrConflict
	}
	if rec.result.State == Issued {
		return rec.result, nil
	}
	if c.pending != "" {
		return Result{State: rec.result.State}, ErrNeedsReconciliation
	}
	unknown := Result{State: OutcomeUnknown}
	c.pending = req.SaleID
	c.records[req.SaleID] = record{req, unknown}

	doc, err := c.device.Issue(ctx, req)
	if err != nil {
		return unknown, fmt.Errorf("%w: device submission: %w", ErrNeedsReconciliation, err)
	}
	if !c.valid(req, doc) {
		return unknown, fmt.Errorf("%w: device returned an unmatched document", ErrNeedsReconciliation)
	}
	return c.finish(req, Result{State: Issued, Document: doc}), nil
}

// Reconcile asks the device about a prior attempt. It never retries issuance.
func (c *Controller) Reconcile(ctx context.Context, saleID string) (Result, error) {
	if saleID == "" {
		return Result{}, ErrInvalid
	}
	if err := c.lock(ctx); err != nil {
		return Result{}, err
	}
	defer c.unlock()
	rec, exists := c.records[saleID]
	if !exists {
		return Result{}, ErrNoAttempt
	}
	if rec.result.State != OutcomeUnknown {
		return rec.result, nil
	}
	observed, err := c.device.Lookup(ctx, rec.request)
	if err != nil {
		return rec.result, fmt.Errorf("%w: device lookup: %w", ErrNeedsReconciliation, err)
	}
	switch {
	case observed.State == Found && c.valid(rec.request, observed.Document):
		return c.finish(rec.request, Result{State: Issued, Document: observed.Document}), nil
	case observed.State == NotIssued && observed.Document == (Document{}):
		return c.finish(rec.request, Result{State: ConfirmedNotIssued}), nil
	default:
		return rec.result, ErrNeedsReconciliation
	}
}

func (c *Controller) finish(req Request, result Result) Result {
	c.records[req.SaleID] = record{req, result}
	if result.State == Issued {
		c.issuedNumbers[documentNumber{result.Document.Kind, result.Document.Number}] = req.SaleID
	}
	c.pending = ""
	return result
}
