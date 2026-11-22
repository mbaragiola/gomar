package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"
)

type POSPriceChange struct {
	ProductID        int64 `json:"product_id"`
	ExpectedRevision int64 `json:"expected_revision" jsonschema:"Revisión exacta devuelta por gomar_pos_catalog."`
	PriceMinor       int64 `json:"price_minor" jsonschema:"Nuevo precio entero en centavos de ARS, de 1 a 100000000."`
}

type POSAvailabilityChange struct {
	ProductID        int64 `json:"product_id"`
	ExpectedRevision int64 `json:"expected_revision" jsonschema:"Revisión exacta devuelta por gomar_pos_catalog."`
	Active           *bool `json:"active" jsonschema:"Disponibilidad: true o false de forma explícita."`
}

func validPOSCatalogProposal(entity, outlet, product, revision int64, requestID string) bool {
	return entity > 0 && outlet > 0 && product > 0 && revision > 0 &&
		revision != math.MaxInt64 && proposalRequestID.MatchString(requestID)
}

func (c *ProposalClient) ProposePOSPrice(ctx context.Context, entity, outlet int64, requestID string, change POSPriceChange) (ProposalReceipt, error) {
	if !validPOSCatalogProposal(entity, outlet, change.ProductID, change.ExpectedRevision, requestID) ||
		change.PriceMinor < 1 || change.PriceMinor > 100000000 {
		return ProposalReceipt{}, errors.New("indicá empresa, sucursal, producto, revisión, precio en centavos y request_id UUIDv4")
	}
	return postPOSCatalogProposal(ctx, c, entity, outlet, requestID, "/pos-price-proposals", change)
}

func (c *ProposalClient) ProposePOSAvailability(ctx context.Context, entity, outlet int64, requestID string, change POSAvailabilityChange) (ProposalReceipt, error) {
	if !validPOSCatalogProposal(entity, outlet, change.ProductID, change.ExpectedRevision, requestID) || change.Active == nil {
		return ProposalReceipt{}, errors.New("indicá empresa, sucursal, producto, revisión, disponibilidad y request_id UUIDv4")
	}
	return postPOSCatalogProposal(ctx, c, entity, outlet, requestID, "/pos-availability-proposals", change)
}

func postPOSCatalogProposal[C POSPriceChange | POSAvailabilityChange](ctx context.Context, c *ProposalClient, entity, outlet int64, requestID, path string, change C) (ProposalReceipt, error) {
	payload, err := json.Marshal(struct {
		RequestID string `json:"request_id"`
		Change    C      `json:"change"`
	}{requestID, change})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, path, payload)
}
