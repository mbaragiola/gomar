package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reservationDateArgs struct {
	outletArgs
	Date string `json:"date" jsonschema:"Día de Buenos Aires en formato AAAA-MM-DD"`
}

type movementArgs struct {
	outletArgs
	ProductID int64  `json:"product_id" jsonschema:"ID exacto devuelto por gomar_list_stock"`
	StartDate string `json:"start_date" jsonschema:"Primer día del período en Buenos Aires, formato AAAA-MM-DD"`
	Days      int    `json:"days" jsonschema:"Duración de 1 a 31 días incluyendo el primer día"`
}

func addBookingReadTools(server *mcp.Server, client *mcpclient.Client) {
	readTool(server, "gomar_reservation_status", "Consultá cuántas solicitudes de reserva hay para cada estado en un día de Buenos Aires, y cuántas personas suman. Sólo para administración de la sucursal; no revela identidades ni confirma reservas.", func(ctx context.Context, in reservationDateArgs) (mcpclient.ReservationStatus, error) {
		return client.ReservationStatus(ctx, in.EntityID, in.OutletID, in.Date)
	})
	readTool(server, "gomar_read_reservation_setup", "Consultá si el local tiene reservas configuradas, si esa configuración está habilitada, si Gomar habilita las reservas públicas y qué zona horaria usa. Solo para administración del local. menu_saved indica si hay un menú guardado incluso si no está publicado; si falta el campo, el servidor no lo informó. No devuelve el archivo ni sus metadatos. Las opciones activas no garantizan que haya horarios, mesas ni una página accesible. No muestra datos privados ni toma reservas.", func(ctx context.Context, in outletArgs) (mcpclient.ReservationSetup, error) {
		return client.ReservationSetup(ctx, in.EntityID, in.OutletID)
	})
	readTool(server, "gomar_published_menu", "Consultá si el menú PDF está publicado para la sucursal. Devuelve sólo disponibilidad y datos del archivo publicado; no descarga ni envía el PDF.", func(ctx context.Context, in outletArgs) (mcpclient.PublishedMenu, error) {
		return client.PublishedMenu(ctx, in.EntityID, in.OutletID)
	})
	readTool(server, "gomar_pos_catalog", "Consultá nombres, precios en centavos de ARS y estado de los productos del catálogo de caja. Devuelve hasta 30 por página; usá next_product_id para seguir.", func(ctx context.Context, in stockArgs) (mcpclient.POSCatalog, error) {
		return client.POSCatalog(ctx, in.EntityID, in.OutletID, in.AfterProductID)
	})
	readTool(server, "gomar_stock_movements", "Resumí los movimientos registrados de un producto de stock por tipo y variación neta de unidades, para hasta 31 días de Buenos Aires. No devuelve actores ni referencias a ventas.", func(ctx context.Context, in movementArgs) (mcpclient.StockMovements, error) {
		return client.StockMovements(ctx, in.EntityID, in.OutletID, in.ProductID, in.StartDate, in.Days)
	})
}
