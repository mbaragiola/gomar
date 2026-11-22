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

type SupplierLegalNameChange struct {
	SupplierID       int64  `json:"supplier_id" jsonschema:"ID exacto del proveedor."`
	ExpectedRevision int64  `json:"expected_revision" jsonschema:"Revisión actual del proveedor, mayor que cero."`
	Name             string `json:"name" jsonschema:"Nueva razón social, de 1 a 160 caracteres."`
}

func (c *ProposalClient) ProposeSupplierLegalNameChange(ctx context.Context, entity, outlet int64, requestID string, supplier SupplierLegalNameChange) (ProposalReceipt, error) {
	supplier.Name = strings.TrimSpace(supplier.Name)
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) || supplier.SupplierID < 1 ||
		supplier.ExpectedRevision < 1 || supplier.ExpectedRevision == math.MaxInt64 || supplier.Name == "" ||
		!utf8.ValidString(supplier.Name) || utf8.RuneCountInString(supplier.Name) > 160 || strings.IndexFunc(supplier.Name, unicode.IsControl) >= 0 {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, proveedor, revisión actual, razón social y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string                  `json:"request_id"`
		Supplier  SupplierLegalNameChange `json:"supplier"`
	}{requestID, supplier})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/supplier-legal-name-proposals", payload)
}
