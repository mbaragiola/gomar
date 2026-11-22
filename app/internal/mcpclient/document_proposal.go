package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type DocumentDraft struct {
	SupplierID int64   `json:"supplier_id" jsonschema:"ID de un proveedor activo del negocio; no ingreses un CUIT."`
	Type       string  `json:"type" jsonschema:"invoice, credit_note o debit_note."`
	Letter     string  `json:"letter,omitempty"`
	Point      int64   `json:"point_of_sale"`
	Number     int64   `json:"number"`
	IssueDate  string  `json:"issue_date" jsonschema:"Fecha AAAA-MM-DD."`
	DueDate    *string `json:"due_date,omitempty" jsonschema:"Fecha AAAA-MM-DD cuando corresponda."`
	Currency   string  `json:"currency" jsonschema:"ARS o USD."`
	Net        *int64  `json:"net_minor,omitempty" jsonschema:"Importe neto entero en centavos."`
	Tax        *int64  `json:"tax_minor,omitempty" jsonschema:"Importe de impuesto entero en centavos."`
	Total      int64   `json:"total_minor" jsonschema:"Total entero en centavos."`
}

func (c *ProposalClient) ProposeDocumentDraft(ctx context.Context, entity, outlet int64, requestID string, draft DocumentDraft) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal y un request_id UUIDv4")
	}
	date := func(value string) bool {
		parsed, err := time.Parse(time.DateOnly, value)
		return err == nil && parsed.Year() >= 1
	}
	draft.Letter = strings.ToUpper(strings.TrimSpace(draft.Letter))
	if draft.SupplierID < 1 || draft.Point < 1 || draft.Point > 99999 || draft.Number < 1 || draft.Number > 99999999 ||
		(draft.Type != "invoice" && draft.Type != "credit_note" && draft.Type != "debit_note") ||
		(draft.Letter != "" && (len(draft.Letter) != 1 || draft.Letter[0] < 'A' || draft.Letter[0] > 'Z')) ||
		(draft.Currency != "ARS" && draft.Currency != "USD") || !date(draft.IssueDate) ||
		(draft.DueDate != nil && !date(*draft.DueDate)) {
		return ProposalReceipt{}, errors.New("revisá los campos del comprobante")
	}
	for _, amount := range []*int64{draft.Net, draft.Tax, &draft.Total} {
		if amount != nil && (*amount < 0 || *amount > 99999999999999) {
			return ProposalReceipt{}, errors.New("usá importes enteros no negativos en centavos")
		}
	}
	payload, err := json.Marshal(struct {
		RequestID string        `json:"request_id"`
		Draft     DocumentDraft `json:"draft"`
	}{requestID, draft})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/document-draft-proposals", payload)
}
