package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type documentNumberProposalArgs struct {
	outletArgs
	RequestID string `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	mcpclient.DocumentNumber
}

func addDocumentNumberProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_document_number_change",
		Description: "Proponé corregir sólo el número impreso (no el punto de venta) de un comprobante en borrador. Usá el ID y revisión actuales de gomar_read_document. La misma sesión de Gomar debe revisar y aprobar; no cambia proveedor, emisor, tipo, punto de venta, importes, archivos ni estado, ni registra deudas o pagos.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args documentNumberProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeDocumentNumber(ctx, args.EntityID, args.OutletID, args.RequestID, args.DocumentNumber)
		return nil, out, err
	})
}
