package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/mbaragiola/gomar/app/internal/posbarcode"
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

type POSBarcodeChange struct {
	ProductID        int64   `json:"product_id"`
	ExpectedRevision int64   `json:"expected_revision" jsonschema:"Revisión exacta del producto."`
	Barcode          *string `json:"barcode" jsonschema:"Código exacto como texto; cadena vacía para quitarlo."`
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

func (c *ProposalClient) ProposePOSBarcode(ctx context.Context, entity, outlet int64, requestID string, change POSBarcodeChange) (ProposalReceipt, error) {
	if !validPOSCatalogProposal(entity, outlet, change.ProductID, change.ExpectedRevision, requestID) ||
		change.Barcode == nil || *change.Barcode != "" && !posbarcode.Valid(*change.Barcode) {
		return ProposalReceipt{}, errors.New("indicá empresa, sucursal, producto, revisión, código exacto como texto y request_id UUIDv4")
	}
	return postPOSCatalogProposal(ctx, c, entity, outlet, requestID, "/pos-barcode-proposals", change)
}

func postPOSCatalogProposal[C POSPriceChange | POSAvailabilityChange | POSBarcodeChange](ctx context.Context, c *ProposalClient, entity, outlet int64, requestID, path string, change C) (ProposalReceipt, error) {
	payload, err := json.Marshal(struct {
		RequestID string `json:"request_id"`
		Change    C      `json:"change"`
	}{requestID, change})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, path, payload)
}
