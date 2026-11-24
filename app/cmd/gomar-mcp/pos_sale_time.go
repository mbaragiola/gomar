package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/mbaragiola/gomar/app/internal/postime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type posSaleTimeArgs struct {
	outletArgs
	RequestID string `json:"request_id" jsonschema:"Referencia UUID exacta visible en Ventas de esta caja; nunca la adivines."`
}

func addPOSSaleTimeTool(server *mcp.Server, client *mcpclient.Client) {
	readTool(server, "gomar_read_pos_sale_time", "Consulta el horario de una venta propia de Caja por su referencia exacta. Requiere permiso de Caja y una conexión fijada a esa sucursal. registered_at_utc es cuando Gomar registró la venta; till_reported_at_utc viene del reloj de la caja y no está verificado. review indica revisar el reloj o la demora de sincronización, no volver a cargar la venta. El reporte mensual usa el registro de Gomar en hora de Buenos Aires. Este horario no acredita cuándo ocurrió la venta, ni un pago ni un comprobante fiscal. El enlace abre solo la consulta en Caja; no registra, cobra ni factura.", func(ctx context.Context, in posSaleTimeArgs) (postime.Evidence, error) {
		return client.ReadPOSSaleTime(ctx, in.EntityID, in.OutletID, in.RequestID)
	})
}
