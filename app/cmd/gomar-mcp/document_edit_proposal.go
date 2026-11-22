package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type documentEditProposalArgs struct {
	outletArgs
	RequestID string                 `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Edit      mcpclient.DocumentEdit `json:"edit"`
}

func addDocumentEditProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_document_edit",
		Description: "Proponé cambiar las fechas y los importes de un comprobante existente en borrador. La misma sesión de Gomar debe revisar y aprobar. No cambia proveedor, moneda, archivos ni estado, y no registra deudas o pagos.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args documentEditProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeDocumentEdit(ctx, args.EntityID, args.OutletID, args.RequestID, args.Edit)
		return nil, out, err
	})
}
