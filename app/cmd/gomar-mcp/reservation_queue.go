package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reservationQueueArgs struct {
	reservationDateArgs
	AfterID string `json:"after_id,omitempty" jsonschema:"Usá el next_id de la página anterior, con la misma sucursal y fecha."`
}
type reservationRequestArgs struct {
	reservationDateArgs
	ReservationID string `json:"reservation_id" jsonschema:"ID exacto devuelto por gomar_list_reservation_requests para el mismo día y sucursal."`
}
type reservationAvailabilityArgs struct {
	reservationDateArgs
	PartySize int `json:"party_size" jsonschema:"Cantidad de personas de 1 a 12"`
}

func addReservationQueueTools(server *mcp.Server, c *mcpclient.Client) {
	readTool(server, "gomar_reservation_availability", "Consultá los horarios que muestra la reserva pública para una sucursal que administrás, para 1 a 12 personas y un día de Buenos Aires entre hoy y 60 días. Los horarios pueden cambiar: la consulta no reserva, no confirma ni garantiza que se pueda ingresar una solicitud.", func(ctx context.Context, in reservationAvailabilityArgs) (mcpclient.ReservationAvailability, error) {
		return c.ReservationAvailability(ctx, in.EntityID, in.OutletID, in.Date, in.PartySize)
	})
	readTool(server, "gomar_list_reservation_requests", "Listá hasta 30 solicitudes para un día de Buenos Aires, en una sucursal que administrás. Sólo devuelve ID, estado, cantidad de personas y hora. Para seguir usá next_id con la misma fecha; no confirma ni cancela reservas.", func(ctx context.Context, in reservationQueueArgs) (mcpclient.ReservationRequests, error) {
		return c.ListReservationRequests(ctx, in.EntityID, in.OutletID, in.Date, in.AfterID)
	})
	readTool(server, "gomar_read_reservation_request", "Consultá una solicitud por su ID y día de Buenos Aires. Sólo en una sucursal que administrás. No devuelve nombres, contactos, mesas ni notas; no modifica reservas.", func(ctx context.Context, in reservationRequestArgs) (mcpclient.ReservationRequest, error) {
		return c.ReadReservationRequest(ctx, in.EntityID, in.OutletID, in.Date, in.ReservationID)
	})
}
