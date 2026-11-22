package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
)

type StockMinimumChange struct {
	ProductID        int64  `json:"product_id" jsonschema:"ID del producto en esta sucursal."`
	ExpectedRevision int64  `json:"expected_revision" jsonschema:"Versión actual del producto en caja."`
	Minimum          *int64 `json:"minimum" jsonschema:"Umbral entero de 0 a 1000000 inclusive. Indicá 0 en forma explícita."`
}

func (c *ProposalClient) ProposeStockMinimumChange(ctx context.Context, entity, outlet int64, requestID string, change StockMinimumChange) (ProposalReceipt, error) {
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) ||
		change.ProductID <= 0 || change.ExpectedRevision <= 0 || change.ExpectedRevision == 1<<63-1 ||
		change.Minimum == nil || *change.Minimum < 0 || *change.Minimum > 1000000 {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, producto, versión, mínimo entero entre 0 y 1000000 y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string             `json:"request_id"`
		Change    StockMinimumChange `json:"minimum_change"`
	}{requestID, change})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/stock-minimum-proposals", payload)
}
