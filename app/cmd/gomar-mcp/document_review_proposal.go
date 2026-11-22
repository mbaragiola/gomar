package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type documentReviewProposalArgs struct {
	outletArgs
	RequestID string                   `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Review    mcpclient.DocumentReview `json:"review"`
}

func addDocumentReviewProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_document_review",
		Description: "Proponé marcar como revisado un comprobante en borrador. Requiere la revisión actual, un motivo y confirmación de la misma sesión en Gomar. No publica una deuda ni registra o realiza pagos.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args documentReviewProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeDocumentReview(ctx, args.EntityID, args.OutletID, args.RequestID, args.Review)
		return nil, out, err
	})
}
