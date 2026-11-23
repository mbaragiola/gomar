package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
)

type StockTrackingChange struct {
	ProductID        int64 `json:"product_id" jsonschema:"ID del producto en esta sucursal."`
	ExpectedRevision int64 `json:"expected_revision" jsonschema:"Versión actual del producto en caja."`
	TrackingEnabled  *bool `json:"tracking_enabled" jsonschema:"Indicá explícitamente true para activar o false para desactivar."`
}

func (c *ProposalClient) ProposeStockTrackingChange(ctx context.Context, entity, outlet int64, requestID string, change StockTrackingChange) (ProposalReceipt, error) {
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) ||
		change.ProductID <= 0 || change.ExpectedRevision <= 0 || change.ExpectedRevision == 1<<63-1 ||
		change.TrackingEnabled == nil {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, producto, versión, seguimiento explícito y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string              `json:"request_id"`
		Change    StockTrackingChange `json:"tracking_change"`
	}{requestID, change})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/stock-tracking-proposals", payload)
}
