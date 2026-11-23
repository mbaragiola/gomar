package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"
)

type DocumentNumber struct {
	DocumentID       int64 `json:"document_id" jsonschema:"ID exacto que devuelve gomar_read_document."`
	ExpectedRevision int64 `json:"expected_revision" jsonschema:"Revisión positiva y exacta que devuelve gomar_read_document."`
	Number           int64 `json:"number" jsonschema:"Número impreso del comprobante (1 a 99999999), sin incluir el punto de venta."`
}

func (c *ProposalClient) ProposeDocumentNumber(ctx context.Context, entity, outlet int64, requestID string, number DocumentNumber) (ProposalReceipt, error) {
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) || number.DocumentID <= 0 ||
		number.ExpectedRevision <= 0 || number.ExpectedRevision == math.MaxInt64 || number.Number <= 0 || number.Number > 99_999_999 {
		return ProposalReceipt{}, errors.New("indicá empresa, sucursal, comprobante, revisión positiva, número impreso y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string `json:"request_id"`
		DocumentNumber
	}{requestID, number})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/document-number-proposals", payload)
}
