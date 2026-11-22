package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type documentDetailArgs struct {
	outletArgs
	DocumentID int64 `json:"document_id" jsonschema:"ID exacto devuelto por gomar_list_documents."`
}

type reconciliationRunArgs struct {
	outletArgs
	RunID int64 `json:"run_id" jsonschema:"ID exacto devuelto por gomar_list_reconciliation_runs."`
}

func addDetailReadTools(server *mcp.Server, client *mcpclient.Client) {
	readTool(server, "gomar_read_document", "Leé un comprobante de una sucursal autorizada: estado, revisión, referencia, fechas y valores en centavos. No devuelve proveedor, datos fiscales del proveedor, notas, adjuntos ni auditoría. No revisa el documento.", func(ctx context.Context, in documentDetailArgs) (mcpclient.DocumentDetails, error) {
		return client.ReadDocument(ctx, in.EntityID, in.OutletID, in.DocumentID)
	})
	readTool(server, "gomar_list_reconciliation_runs", "Listá conciliaciones que incluyen esta sucursal y que podés consultar en todas las sucursales participantes. Devuelve estado, revisión, fechas y cantidades, sin datos de cuentas ni archivos. Hasta 30; usá next_id para continuar.", func(ctx context.Context, in accountingPageArgs) (mcpclient.ReconciliationRuns, error) {
		return client.ListReconciliationRuns(ctx, in.EntityID, in.OutletID, in.AfterID)
	})
	readTool(server, "gomar_read_reconciliation_run", "Leé estado, revisión, fechas y cantidades de una conciliación. Requiere acceso a todas las sucursales participantes. No devuelve cuentas, CSV ni filas individuales.", func(ctx context.Context, in reconciliationRunArgs) (mcpclient.ReconciliationRun, error) {
		return client.ReadReconciliationRun(ctx, in.EntityID, in.OutletID, in.RunID)
	})
	readTool(server, "gomar_read_reconciliation_setup", "Consultá sólo la configuración de una conciliación: estado, revisión, presencia de fuentes, regla de referencia y límites conocidos. Requiere acceso a todas las sucursales participantes. No lee archivos, referencias ni filas y no confirma que la conciliación se pueda preparar.", func(ctx context.Context, in reconciliationRunArgs) (mcpclient.ReconciliationSetup, error) {
		return client.ReadReconciliationSetup(ctx, in.EntityID, in.OutletID, in.RunID)
	})

}
