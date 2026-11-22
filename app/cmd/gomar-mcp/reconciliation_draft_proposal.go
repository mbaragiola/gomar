package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reconciliationDraftProposalArgs struct {
	outletArgs
	RequestID string                        `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Draft     mcpclient.ReconciliationDraft `json:"draft"`
}

func addReconciliationDraftProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_reconciliation_draft",
		Description: "Proponé un borrador vacío de conciliación para una única sucursal. Gomar necesita confirmación de la sesión que originó la propuesta. No carga extractos, concilia movimientos, cierra el borrador ni realiza pagos.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args reconciliationDraftProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeReconciliationDraft(ctx, args.EntityID, args.OutletID, args.RequestID, args.Draft)
		return nil, out, err
	})
}
