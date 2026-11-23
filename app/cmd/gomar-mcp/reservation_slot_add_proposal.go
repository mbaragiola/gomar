package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reservationSlotAddProposalArgs struct {
	outletArgs
	RequestID string                             `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Change    mcpclient.ReservationSlotAddChange `json:"slot_add"`
}

func addReservationSlotAddProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_reservation_slot_add",
		Description: "Proponé agregar un turno futuro a un local de reservas ya configurado y desactivado. Solo la misma sesión de Gomar puede aprobar. No habilita reservas ni cambia solicitudes. Gomar no permite retirar el turno.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args reservationSlotAddProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeReservationSlotAdd(ctx, args.EntityID, args.OutletID, args.RequestID, args.Change)
		return nil, out, err
	})
}
