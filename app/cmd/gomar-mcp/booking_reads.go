package main

import (
	"context"
	"errors"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reservationDateArgs struct {
	outletArgs
	Date string `json:"date" jsonschema:"Día de Buenos Aires en formato AAAA-MM-DD"`
}

type reservationStartSetupArgs struct {
	outletArgs
	StartAt string `json:"start_at" jsonschema:"Un inicio exacto futuro en UTC canónico AAAA-MM-DDTHH:MM:00Z, del horario del local (Buenos Aires)."`
}

type reservationTableSetupArgs struct {
	outletArgs
	TableNumber int `json:"table_number" jsonschema:"Número de una mesa Mesa N; entero de 1 a 9999."`
}

type posCatalogListArgs struct {
	stockArgs
	Barcode *string `json:"barcode,omitempty" jsonschema:"Código de barras exacto como texto (conservá ceros iniciales); sin cursor."`
}

type posCatalogProductArgs struct {
	outletArgs
	ProductID int64 `json:"product_id" jsonschema:"ID exacto devuelto por gomar_pos_catalog de esta sucursal."`
}

type posTableCheckArgs struct {
	outletArgs
	TableNumber int64 `json:"table_number" jsonschema:"Número exacto de mesa en caja, entero de 1 a 9999."`
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
	readTool(server, "gomar_read_reservation_start_setup", "Consultá si un inicio exacto ya está configurado para reservas en este local, incluso si está desactivado. Solo para administración del local. La hora debe ser futura, en la grilla de 90 minutos y dentro de los 60 días de Buenos Aires. Devuelve solo si está configurado; no comprueba disponibilidad, mesas ni solicitudes y no realiza cambios.", func(ctx context.Context, in reservationStartSetupArgs) (mcpclient.ReservationStartSetup, error) {
		return client.ReservationStartSetup(ctx, in.EntityID, in.OutletID, in.StartAt)
	})
	readTool(server, "gomar_read_reservation_table_setup", "Consultá si la mesa numerada indicada está configurada en este local, incluso si está desactivado. Solo para administración del local. Si coinciden varios nombres con el mismo número, table_configured es true pero no se devuelve capacity. No comprueba disponibilidad ni ocupación, no toma reservas y no realiza cambios.", func(ctx context.Context, in reservationTableSetupArgs) (mcpclient.ReservationTableSetup, error) {
		return client.ReservationTableSetup(ctx, in.EntityID, in.OutletID, in.TableNumber)
	})
	readTool(server, "gomar_published_menu", "Consultá si el menú PDF está publicado para la sucursal. Devuelve sólo disponibilidad y datos del archivo publicado; no descarga ni envía el PDF.", func(ctx context.Context, in outletArgs) (mcpclient.PublishedMenu, error) {
		return client.PublishedMenu(ctx, in.EntityID, in.OutletID)
	})
	readTool(server, "gomar_pos_catalog", "Consultá nombres, precios, disponibilidad y códigos de barras del catálogo de caja de la sucursal. Paginá con next_product_id o buscá por código exacto, sin cursor.", func(ctx context.Context, in posCatalogListArgs) (mcpclient.POSCatalog, error) {
		if in.Barcode != nil {
			if in.AfterProductID != nil {
				return mcpclient.POSCatalog{}, errors.New("no combines un código de barras con el cursor")
			}
			return client.POSCatalogByBarcode(ctx, in.EntityID, in.OutletID, *in.Barcode)
		}
		return client.POSCatalog(ctx, in.EntityID, in.OutletID, in.AfterProductID)
	})
	readTool(server, "gomar_read_pos_product", "Consultá un producto por su ID exacto en el catálogo de caja de esta sucursal. Devuelve nombre, precio en centavos de ARS, revisión, código asignado y estado, incluso si está inactivo. No busca productos ni devuelve stock, costos o ventas.", func(ctx context.Context, in posCatalogProductArgs) (mcpclient.POSProductDetails, error) {
		return client.ReadPOSCatalogProduct(ctx, in.EntityID, in.OutletID, in.ProductID)
	})
	readTool(server, "gomar_read_pos_table_check", "Consultá el total en centavos y el estado de una cuenta de mesa guardada en Caja. Requiere una conexión fijada a ese local y permiso de caja. Devuelve un enlace para abrir la cuenta en Gomar, sin productos ni datos de pago. Si hay anotaciones manuales, no prueban que haya un pago. No crea, cobra ni factura.", func(ctx context.Context, in posTableCheckArgs) (mcpclient.POSTableCheck, error) {
		return client.ReadPOSTableCheck(ctx, in.EntityID, in.OutletID, in.TableNumber)
	})
	readTool(server, "gomar_stock_movements", "Resumí los movimientos registrados de un producto de stock por tipo y variación neta de unidades, para hasta 31 días de Buenos Aires. No devuelve actores ni referencias a ventas.", func(ctx context.Context, in movementArgs) (mcpclient.StockMovements, error) {
		return client.StockMovements(ctx, in.EntityID, in.OutletID, in.ProductID, in.StartDate, in.Days)
	})
}
