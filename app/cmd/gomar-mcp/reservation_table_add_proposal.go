package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reservationTableAddProposalArgs struct {
	outletArgs
	RequestID string                              `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Change    mcpclient.ReservationTableAddChange `json:"table_add"`
}

func addReservationTableAddProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_reservation_table_add",
		Description: "Proponé agregar una mesa numerada a un local de reservas ya configurado y desactivado. Solo la misma sesión de Gomar puede aprobar. No habilita reservas, agrega turnos ni cambia solicitudes. La mesa no se puede retirar desde Gomar.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args reservationTableAddProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeReservationTableAdd(ctx, args.EntityID, args.OutletID, args.RequestID, args.Change)
		return nil, out, err
	})
}
