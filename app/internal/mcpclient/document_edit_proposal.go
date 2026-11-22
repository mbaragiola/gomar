package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type DocumentEditValues struct {
	IssueDate string  `json:"issue_date" jsonschema:"Fecha de emisión en formato AAAA-MM-DD."`
	DueDate   *string `json:"due_date" jsonschema:"Fecha de vencimiento AAAA-MM-DD o null."`
	Net       *int64  `json:"net_minor" jsonschema:"Importe neto en centavos, o null junto con tax_minor."`
	Tax       *int64  `json:"tax_minor" jsonschema:"Impuesto en centavos, o null junto con net_minor."`
	Total     *int64  `json:"total_minor" jsonschema:"Total entero no negativo en centavos."`
}

type DocumentEdit struct {
	DocumentID       int64              `json:"document_id"`
	ExpectedRevision *int64             `json:"expected_revision" jsonschema:"Revisión exacta que leíste, incluida 0."`
	Reason           string             `json:"reason" jsonschema:"Motivo explícito en texto plano, sin credenciales ni datos personales."`
	After            DocumentEditValues `json:"after"`
}

func (c *ProposalClient) ProposeDocumentEdit(ctx context.Context, entity, outlet int64, requestID string, edit DocumentEdit) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) ||
		edit.DocumentID < 1 || edit.ExpectedRevision == nil || *edit.ExpectedRevision < 0 || *edit.ExpectedRevision == math.MaxInt64 {
		return ProposalReceipt{}, errors.New("indicá empresa, sucursal, comprobante, revisión y un request_id UUIDv4")
	}
	edit.Reason = strings.TrimFunc(edit.Reason, func(r rune) bool {
		return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
	})
	if edit.Reason == "" || len(edit.Reason) > 1024 || !utf8.ValidString(edit.Reason) || strings.ContainsRune(edit.Reason, 0) {
		return ProposalReceipt{}, errors.New("indicá un motivo breve en texto plano")
	}
	v := edit.After
	issue, err := time.Parse(time.DateOnly, v.IssueDate)
	if err != nil || issue.Year() < 1 {
		return ProposalReceipt{}, errors.New("indicá una fecha de emisión válida")
	}
	if v.DueDate != nil {
		due, err := time.Parse(time.DateOnly, *v.DueDate)
		if err != nil || due.Before(issue) {
			return ProposalReceipt{}, errors.New("el vencimiento debe ser igual o posterior a la emisión")
		}
	}
	for _, amount := range []*int64{v.Net, v.Tax, v.Total} {
		if amount != nil && (*amount < 0 || *amount > 99999999999999) {
			return ProposalReceipt{}, errors.New("usá importes enteros no negativos en centavos")
		}
	}
	if v.Total == nil || (v.Net == nil) != (v.Tax == nil) || (v.Net != nil && *v.Net+*v.Tax != *v.Total) {
		return ProposalReceipt{}, errors.New("indicá el total y, si desglosás, un neto e impuesto que sumen el total")
	}
	payload, err := json.Marshal(struct {
		RequestID string       `json:"request_id"`
		Edit      DocumentEdit `json:"edit"`
	}{requestID, edit})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/document-edit-proposals", payload)
}
