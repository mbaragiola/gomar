package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
)

type StockReceipt struct {
	ProductID int64 `json:"product_id" jsonschema:"ID del producto de stock activo."`
	Quantity  int64 `json:"quantity" jsonschema:"Cantidad entera positiva que se suma, entre 1 y 1000000 unidades."`
}

func (c *ProposalClient) ProposeStockReceipt(ctx context.Context, entity, outlet int64, requestID string, receipt StockReceipt) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) || receipt.ProductID < 1 ||
		receipt.Quantity < 1 || receipt.Quantity > 1000000 {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, producto, cantidad positiva y un request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string       `json:"request_id"`
		Receipt   StockReceipt `json:"receipt"`
	}{requestID, receipt})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/stock-receipt-proposals", payload)
}
