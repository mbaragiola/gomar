package fiscalcontroller

import (
	"context"
	"errors"
	"sync"
)

type NextIssue uint8

const (
	IssueNormally NextIssue = iota
	LoseResponseBeforeIssue
	LoseResponseAfterIssue
)

var errSimulatedConnectionLoss = errors.New("simulated loss of connection to fiscal controller")

// Simulator is an in-memory fake. Its documents are not fiscal authorizations.
// Unlike a real controller, it can authoritatively find documents by sale ID.
type Simulator struct {
	mu          sync.Mutex
	pointOfSale int
	documents   map[string]Document
	lastNumber  int64
	nextIssue   NextIssue
	hideLookup  bool
}

func NewSimulator(pointOfSale int) *Simulator {
	return &Simulator{pointOfSale: pointOfSale, documents: make(map[string]Document)}
}

func (s *Simulator) FailNextIssue(how NextIssue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextIssue = how
}

func (s *Simulator) SetLookupInconclusive(hide bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hideLookup = hide
}

func (s *Simulator) Issue(ctx context.Context, req Request) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	how := s.nextIssue
	s.nextIssue = IssueNormally
	if how == LoseResponseBeforeIssue {
		return Document{}, errSimulatedConnectionLoss
	}
	s.lastNumber++
	doc := Document{SaleID: req.SaleID, Kind: req.Kind, PointOfSale: s.pointOfSale, Number: s.lastNumber, TotalCentavos: req.TotalCentavos}
	s.documents[req.SaleID] = doc
	if how == LoseResponseAfterIssue {
		return Document{}, errSimulatedConnectionLoss
	}
	return doc, nil
}

func (s *Simulator) Lookup(ctx context.Context, req Request) (LookupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return LookupResult{}, err
	}
	if s.hideLookup {
		return LookupResult{State: Inconclusive}, nil
	}
	if doc, ok := s.documents[req.SaleID]; ok {
		return LookupResult{State: Found, Document: doc}, nil
	}
	return LookupResult{State: NotIssued}, nil
}
