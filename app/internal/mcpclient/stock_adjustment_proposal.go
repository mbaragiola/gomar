package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
)

type StockAdjustment struct {
	ProductID int64  `json:"product_id" jsonschema:"ID exacto del producto con stock habilitado."`
	Quantity  *int64 `json:"quantity" jsonschema:"Cantidad entera. Para un conteo indicá incluso 0 de forma explícita."`
}

func (c *ProposalClient) ProposeStockWaste(ctx context.Context, entity, outlet int64, requestID string, adjustment StockAdjustment) (ProposalReceipt, error) {
	return c.proposeStockAdjustment(ctx, entity, outlet, requestID, adjustment, 1, "/stock-waste-proposals")
}

func (c *ProposalClient) ProposeStockPhysicalCount(ctx context.Context, entity, outlet int64, requestID string, adjustment StockAdjustment) (ProposalReceipt, error) {
	return c.proposeStockAdjustment(ctx, entity, outlet, requestID, adjustment, 0, "/stock-physical-count-proposals")
}

func (c *ProposalClient) proposeStockAdjustment(ctx context.Context, entity, outlet int64, requestID string, adjustment StockAdjustment, minimum int64, path string) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) || adjustment.ProductID < 1 ||
		adjustment.Quantity == nil || *adjustment.Quantity < minimum || *adjustment.Quantity > 1000000 {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, producto, cantidad válida y un request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID  string          `json:"request_id"`
		Adjustment StockAdjustment `json:"adjustment"`
	}{requestID, adjustment})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, path, payload)
}
