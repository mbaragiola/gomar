package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

type DocumentReview struct {
	DocumentID       int64  `json:"document_id" jsonschema:"ID exacto del borrador devuelto por gomar_read_document."`
	ExpectedRevision int64  `json:"expected_revision" jsonschema:"Revisión actual del borrador; no la inventes."`
	Reason           string `json:"reason" jsonschema:"Motivo de revisión, obligatorio y en texto plano. No incluyas credenciales ni datos personales."`
}

func (c *ProposalClient) ProposeDocumentReview(ctx context.Context, entity, outlet int64, requestID string, review DocumentReview) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) ||
		review.DocumentID < 1 || review.ExpectedRevision < 1 || review.ExpectedRevision == math.MaxInt64 {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, comprobante, revisión y un request_id UUIDv4")
	}
	review.Reason = strings.TrimFunc(review.Reason, func(r rune) bool {
		return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
	})
	if review.Reason == "" || len(review.Reason) > 1024 || !utf8.ValidString(review.Reason) || strings.ContainsRune(review.Reason, 0) {
		return ProposalReceipt{}, errors.New("indicá un motivo de revisión breve en texto plano")
	}
	payload, err := json.Marshal(struct {
		RequestID string         `json:"request_id"`
		Review    DocumentReview `json:"review"`
	}{requestID, review})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/document-review-proposals", payload)
}
