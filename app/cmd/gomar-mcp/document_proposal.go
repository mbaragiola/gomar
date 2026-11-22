package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type documentProposalArgs struct {
	outletArgs
	RequestID string                  `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Draft     mcpclient.DocumentDraft `json:"draft" jsonschema:"Comprobante para dejar como borrador. No incluyas CUIT, archivos, datos bancarios ni credenciales."`
}

func addDocumentProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_document_draft",
		Description: "Dejá una propuesta para crear un borrador de comprobante de un proveedor existente. La persona que lo solicitó debe revisar los campos y aprobar en Gomar. No crea el borrador automáticamente, no revisa el comprobante y no registra ni paga deudas.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args documentProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeDocumentDraft(ctx, args.EntityID, args.OutletID, args.RequestID, args.Draft)
		return nil, out, err
	})
}
