package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The zero value is reserved for omission; explicit null or an empty string
// must not turn a filtered financial query into the unfiltered operation.
type payableDateArg string

func (d *payableDateArg) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return errors.New("indicá una fecha de vencimiento YYYY-MM-DD")
	}
	*d = payableDateArg(value)
	return nil
}

type payablePageArgs struct {
	accountingPageArgs
	DueFrom payableDateArg `json:"due_from,omitempty" jsonschema:"Fecha local de vencimiento desde (inclusive), YYYY-MM-DD; requiere due_to."`
	DueTo   payableDateArg `json:"due_to,omitempty" jsonschema:"Fecha local de vencimiento hasta (inclusive), YYYY-MM-DD; requiere due_from."`
}

type payableDetailArgs struct {
	outletArgs
	PayableID int64 `json:"payable_id" jsonschema:"ID de gomar_list_payables."`
}
type payableMovementArgs struct {
	payableDetailArgs
	Cursor *string `json:"cursor,omitempty" jsonschema:"next_cursor de la página anterior de esta misma cuenta; omitilo al empezar."`
}
type recordedPaymentArgs struct {
	outletArgs
	PaymentID int64 `json:"payment_id" jsonschema:"ID de gomar_list_recorded_payments."`
}
type stockMovementDetailArgs struct {
	movementArgs
	AfterID *int64 `json:"after_id,omitempty" jsonschema:"Cursor next_id de la página anterior; omitilo al empezar."`
}

func addFinanceStockReadTools(server *mcp.Server, client *mcpclient.Client) {
	readTool(server, "gomar_list_payables", "Listá cuentas a pagar de una sucursal autorizada, con fechas y centavos por moneda. Omite cuentas con aplicaciones de otra sucursal. Hasta 30 en orden de ID. Podés filtrar por vencimiento con due_from y due_to (fechas locales inclusive; excluye cuentas sin vencimiento). Para seguir usá next_id y mantené las mismas fechas.", func(ctx context.Context, in payablePageArgs) (mcpclient.FinanceRecords[mcpclient.PayableRecord], error) {
		if in.DueFrom == "" && in.DueTo == "" {
			return client.ListPayables(ctx, in.EntityID, in.OutletID, in.AfterID)
		}
		return client.ListPayablesDue(ctx, in.EntityID, in.OutletID, in.AfterID, string(in.DueFrom), string(in.DueTo))
	})
	readTool(server, "gomar_read_payable", "Consultá el saldo y estado de una cuenta a pagar de esta sucursal. Excluye aplicaciones de otra sucursal. No incluye proveedor, comprobante ni medio de pago.", func(ctx context.Context, in payableDetailArgs) (mcpclient.PayableRecord, error) {
		return client.ReadPayable(ctx, in.EntityID, in.OutletID, in.PayableID)
	})
	readTool(server, "gomar_list_payable_movements", "Explicá el historial de una cuenta a pagar de esta sucursal: alta, aplicaciones internas y reversiones; fecha local cuando existe, cambio y saldo resultante en centavos. No confirma movimientos bancarios ni incluye borradores o identificadores de pagos. Hasta 30 en orden de registro; usá next_cursor solo en la misma cuenta.", func(ctx context.Context, in payableMovementArgs) (mcpclient.PayableMovements, error) {
		return client.ListPayableMovements(ctx, in.EntityID, in.OutletID, in.PayableID, in.Cursor)
	})
	readTool(server, "gomar_list_recorded_payments", "Listá los registros internos de pagos de esta sucursal con fechas y centavos. No confirma transferencias bancarias. Excluye aplicaciones a otras sucursales, métodos, referencias y recibos. Hasta 30; usá next_id.", func(ctx context.Context, in accountingPageArgs) (mcpclient.FinanceRecords[mcpclient.RecordedPayment], error) {
		return client.ListRecordedPayments(ctx, in.EntityID, in.OutletID, in.AfterID)
	})
	readTool(server, "gomar_read_recorded_payment", "Consultá un registro interno de pago por ID autorizado. No confirma un pago en el banco ni muestra proveedor, método, referencias o recibos.", func(ctx context.Context, in recordedPaymentArgs) (mcpclient.RecordedPayment, error) {
		return client.ReadRecordedPayment(ctx, in.EntityID, in.OutletID, in.PaymentID)
	})
	readTool(server, "gomar_list_stock_movements", "Listá tipo, cambio neto y hora local de movimientos registrados para un producto de esta sucursal. Período de 1 a 31 días; hasta 30 movimientos, con next_id. No devuelve personas ni pedidos.", func(ctx context.Context, in stockMovementDetailArgs) (mcpclient.StockMovementDetails, error) {
		return client.ListStockMovements(ctx, in.EntityID, in.OutletID, in.ProductID, in.StartDate, in.Days, in.AfterID)
	})
}
