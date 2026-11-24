package gomarmcp

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type noArgs struct{}
type entityArgs struct {
	EntityID int64 `json:"entity_id" jsonschema:"ID exacto devuelto por gomar_list_entities"`
}
type outletArgs struct {
	EntityID int64 `json:"entity_id" jsonschema:"ID exacto devuelto por gomar_list_entities"`
	OutletID int64 `json:"outlet_id" jsonschema:"ID exacto devuelto por gomar_list_outlets"`
}

// AddReadTool registers a read-only Gomar tool and its typed result.
func AddReadTool[I, O any](server *mcp.Server, name, description string, query func(context.Context, I) (O, error)) {
	closed := false
	mcp.AddTool(server, &mcp.Tool{
		Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args I) (*mcp.CallToolResult, O, error) {
		value, err := query(ctx, args)
		return nil, value, err
	})
}

// AddDiscoveryTools registers the common business and outlet discoveries.
func AddDiscoveryTools(server *mcp.Server, client *mcpclient.Client) {
	AddReadTool(server, "gomar_list_entities", "Lista sólo las empresas a las que tenés acceso mediante esta conexión.", func(ctx context.Context, _ noArgs) (mcpclient.Entities, error) { return client.Entities(ctx) })
	AddReadTool(server, "gomar_list_outlets", "Lista las sucursales activas y autorizadas de una empresa. Usá el ID de gomar_list_entities.", func(ctx context.Context, in entityArgs) (mcpclient.Outlets, error) {
		return client.Outlets(ctx, in.EntityID)
	})
}

// AddDeliveryTools registers the two tools for reading a delivery inbox.
func AddDeliveryTools(server *mcp.Server, client *mcpclient.Client) {
	AddReadTool(server, "gomar_list_recent_delivery", "Consultá los últimos 30 pedidos registrados en Gomar para una sucursal autorizada. Un pedido recibido no está confirmado. No consulta proveedores en vivo ni muestra datos del cliente.", func(ctx context.Context, in outletArgs) (mcpclient.Delivery, error) {
		return client.Delivery(ctx, in.EntityID, in.OutletID)
	})
	AddReadTool(server, "gomar_delivery_status_summary", "Contá los pedidos recibidos, aceptados y cancelados entre los últimos 100 de la bandeja de delivery de una sucursal autorizada. Es el estado actual en Gomar, no todo el historial ni una consulta al proveedor. No muestra pedidos individuales ni datos de clientes.", func(ctx context.Context, in outletArgs) (mcpclient.DeliveryStatusSummary, error) {
		return client.DeliveryStatus(ctx, in.EntityID, in.OutletID)
	})
}
