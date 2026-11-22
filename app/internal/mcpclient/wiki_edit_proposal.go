package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type WikiEdit struct {
	ArticleID       int64  `json:"article_id" jsonschema:"ID exacto devuelto por gomar_read_knowledge_article."`
	ExpectedVersion int64  `json:"expected_version" jsonschema:"Versión leída de la página. No inventes una versión."`
	Title           string `json:"title" jsonschema:"Título propuesto, hasta 120 caracteres."`
	Body            string `json:"body" jsonschema:"Texto plano propuesto, hasta 12000 caracteres."`
}

func (c *ProposalClient) ProposeWikiEdit(ctx context.Context, entity, outlet int64, requestID string, edit WikiEdit) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) || edit.ArticleID < 1 || edit.ExpectedVersion < 1 {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, página, versión y un request_id UUIDv4")
	}
	edit.Title = strings.TrimSpace(edit.Title)
	if !validWikiProposalText(edit.Title, edit.Body) {
		return ProposalReceipt{}, errors.New("revisá que el título y el contenido respeten los límites de la wiki")
	}
	payload, err := json.Marshal(struct {
		RequestID string   `json:"request_id"`
		Edit      WikiEdit `json:"edit"`
	}{requestID, edit})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/wiki-edit-proposals", payload)
}
