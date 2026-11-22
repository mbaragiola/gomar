package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type supplierLegalNameArgs struct {
	outletArgs
	RequestID string                            `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Supplier  mcpclient.SupplierLegalNameChange `json:"supplier"`
}

func addSupplierLegalNameProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_supplier_legal_name_change",
		Description: "Proponé cambiar sólo la razón social de un proveedor. La sucursal autoriza la propuesta, pero si la misma sesión la aprueba en Gomar el cambio se aplica a toda la empresa. No cambia el CUIT ni los datos de contacto.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args supplierLegalNameArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeSupplierLegalNameChange(ctx, args.EntityID, args.OutletID, args.RequestID, args.Supplier)
		return nil, out, err
	})
}
