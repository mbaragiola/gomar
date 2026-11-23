package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type documentPointOfSaleProposalArgs struct {
	outletArgs
	RequestID string `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	mcpclient.DocumentPointOfSale
}

func addDocumentPointOfSaleProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_document_point_of_sale_change",
		Description: "Proponé corregir sólo el punto de venta impreso de un borrador, no el local ni el número impreso. Usá el ID y revisión actuales de gomar_read_document. La misma sesión de Gomar revisa y aprueba; no cambia proveedor, emisor, tipo, letra, importes, archivos ni estado, ni registra deudas o pagos.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args documentPointOfSaleProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeDocumentPointOfSale(ctx, args.EntityID, args.OutletID, args.RequestID, args.DocumentPointOfSale)
		return nil, out, err
	})
}
