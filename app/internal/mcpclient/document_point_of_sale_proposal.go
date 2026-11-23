package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"
)

type DocumentPointOfSale struct {
	DocumentID       int64 `json:"document_id" jsonschema:"ID exacto que devuelve gomar_read_document."`
	ExpectedRevision int64 `json:"expected_revision" jsonschema:"Revisión positiva y exacta que devuelve gomar_read_document."`
	PointOfSale      int64 `json:"point_of_sale" jsonschema:"Punto de venta impreso del comprobante (1 a 99999), no el número ni el ID del local."`
}

func (c *ProposalClient) ProposeDocumentPointOfSale(ctx context.Context, entity, outlet int64, requestID string, point DocumentPointOfSale) (ProposalReceipt, error) {
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) || point.DocumentID <= 0 ||
		point.ExpectedRevision <= 0 || point.ExpectedRevision == math.MaxInt64 || point.PointOfSale <= 0 || point.PointOfSale > 99_999 {
		return ProposalReceipt{}, errors.New("indicá empresa, sucursal, comprobante, revisión positiva, punto de venta impreso y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string `json:"request_id"`
		DocumentPointOfSale
	}{requestID, point})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/document-point-of-sale-proposals", payload)
}
