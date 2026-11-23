package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const liveUsage = "uso: gomar-mcp --origin=<origen de Gomar> --token-file=<ruta absoluta privada> [--wiki-proposal-token-file=<ruta privada>] [--document-proposal-token-file=<ruta privada>] [--wiki-edit-proposal-token-file=<ruta privada>] [--stock-receipt-proposal-token-file=<ruta privada>] [--proposal-session-token-file=<ruta privada> --proposal-session-action=<acción autorizada>]"

type noArgs struct{}
type entityArgs struct {
	EntityID int64 `json:"entity_id" jsonschema:"ID exacto devuelto por gomar_list_entities"`
}
type salesArgs struct {
	EntityID int64  `json:"entity_id" jsonschema:"ID exacto devuelto por gomar_list_entities"`
	OutletID *int64 `json:"outlet_id,omitempty" jsonschema:"ID exacto devuelto por gomar_list_outlets; si se omite, sólo se incluyen tus sucursales autorizadas"`
	Period   string `json:"period" jsonschema:"this_month para este mes; previous_month para el mes anterior"`
}

func readTool[I, O any](server *mcp.Server, name, description string, query func(context.Context, I) (O, error)) {
	closed := false
	mcp.AddTool(server, &mcp.Tool{
		Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args I) (*mcp.CallToolResult, O, error) {
		value, err := query(ctx, args)
		return nil, value, err
	})
}

func liveServer(client *mcpclient.Client) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "gomar", Version: "0.1.0"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	readTool(server, "gomar_current_user", "Identifica tu cuenta autenticada de Gomar; no muestra credenciales.", func(ctx context.Context, _ noArgs) (mcpclient.User, error) { return client.Me(ctx) })
	readTool(server, "gomar_list_entities", "Lista sólo las empresas a las que tenés acceso mediante esta conexión.", func(ctx context.Context, _ noArgs) (mcpclient.Entities, error) { return client.Entities(ctx) })
	readTool(server, "gomar_list_outlets", "Lista las sucursales activas y autorizadas de una empresa. Usá el ID de gomar_list_entities.", func(ctx context.Context, in entityArgs) (mcpclient.Outlets, error) {
		return client.Outlets(ctx, in.EntityID)
	})
	readTool(server, "gomar_sales_summary", "Responde cuánto vendimos este mes o el anterior a partir de ventas registradas en la caja de Gomar. No incluye pedidos de delivery, facturas de proveedores ni ventas no registradas. Los importes total_minor son centavos de la moneda indicada. Confirmá la empresa con gomar_list_entities y no inventes ventas ni IDs.", func(ctx context.Context, in salesArgs) (mcpclient.Sales, error) {
		return client.Sales(ctx, in.EntityID, in.OutletID, in.Period)
	})
	addWorkspaceTools(server, client)
	addAccountingReadTools(server, client)
	addBookingReadTools(server, client)
	addDetailReadTools(server, client)
	addFinanceStockReadTools(server, client)
	addReservationQueueTools(server, client)
	return server
}

type quietWriter struct{ io.Writer }

func (quietWriter) Close() error { return nil }

func runLive(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("gomar-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	origin := fs.String("origin", "", "")
	tokenFile := fs.String("token-file", "", "")
	proposalFile := fs.String("wiki-proposal-token-file", "", "")
	documentProposalFile := fs.String("document-proposal-token-file", "", "")
	wikiEditProposalFile := fs.String("wiki-edit-proposal-token-file", "", "")
	stockReceiptProposalFile := fs.String("stock-receipt-proposal-token-file", "", "")
	proposalSessionFile := fs.String("proposal-session-token-file", "", "")
	proposalSessionAction := fs.String("proposal-session-action", "pos_product_rename", "")
	var laneFlags proposalLaneFlag
	fs.Var(&laneFlags, "proposal-lane", "")
	laneEntity := fs.Int64("proposal-entity", 0, "")
	laneOutlet := fs.Int64("proposal-outlet", 0, "")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *origin == "" || !filepath.IsAbs(*tokenFile) {
		return errors.New(liveUsage)
	}
	if len(laneFlags) > 0 && repeatedProposalLaneScope(args) {
		return invalidProposalLanes
	}
	lanes, err := parseProposalLanes(fs, laneFlags, *tokenFile, *laneEntity, *laneOutlet)
	if err != nil {
		return err
	}
	var token string
	var pins []os.FileInfo
	if len(lanes) != 0 {
		pins, err = validateProposalLaneFiles(*tokenFile, lanes)
		if err != nil {
			return err
		}
		token, err = readBoundLaneToken(*tokenFile, pins[0])
	} else {
		token, err = mcpclient.ReadToken(*tokenFile)
	}
	if err != nil {
		return err
	}
	client, err := mcpclient.New(*origin, token)
	if err != nil {
		return err
	}
	server := liveServer(client)
	if len(lanes) != 0 {
		if err := attachProposalLanes(server, lanes, pins, *origin, *laneEntity, *laneOutlet, token); err != nil {
			return err
		}
		return server.Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(in), Writer: quietWriter{out}, MaxLineLength: 16 * 1024})
	}
	var addSessionTool func(*mcp.Server, *mcpclient.ProposalClient)
	switch *proposalSessionAction {
	case "pos_product_rename":
		addSessionTool = addPOSRenameProposalTool
	case "document_draft_review":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addDocumentReviewProposalTool
	case "reconciliation_draft_create":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addReconciliationDraftProposalTool
	case "document_draft_point_of_sale_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addDocumentPointOfSaleProposalTool
	case "document_draft_number_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addDocumentNumberProposalTool
	case "document_draft_edit":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addDocumentEditProposalTool
	case "stock_waste_record":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addStockWasteProposalTool
	case "stock_physical_count":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addStockPhysicalCountProposalTool
	case "pos_product_price_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addPOSPriceProposalTool
	case "pos_product_barcode_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addPOSBarcodeProposalTool
	case "pos_product_availability_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addPOSAvailabilityProposalTool
	case "outlet_display_label_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addOutletDisplayLabelProposalTool
	case "reservation_cancellation_policy_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addReservationPolicyNoticeProposalTool
	case "reservation_slot_add":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addReservationSlotAddProposalTool
	case "reservation_table_add":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addReservationTableAddProposalTool
	case "reservation_confirm":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addReservationConfirmProposalTool
	case "reservation_cancel":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addReservationCancelProposalTool
	case "pos_product_create":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addPOSCreateProposalTool
	case "stock_product_setup":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addStockProductSetupProposalTool
	case "stock_tracking_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addStockTrackingProposalTool
	case "stock_minimum_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addStockMinimumProposalTool
	case "supplier_legal_name_change":
		if *proposalSessionFile == "" {
			return errors.New(liveUsage)
		}
		addSessionTool = addSupplierLegalNameProposalTool
	default:
		return errors.New(liveUsage)
	}
	maxLine := 16 * 1024
	usedTokens := map[string]bool{token: true}
	for _, cfg := range []struct {
		file string
		add  func(*mcp.Server, *mcpclient.ProposalClient)
	}{
		{*proposalFile, addWikiProposalTool},
		{*documentProposalFile, addDocumentProposalTool},
		{*wikiEditProposalFile, addWikiEditProposalTool},
		{*stockReceiptProposalFile, addStockReceiptProposalTool},
		{*proposalSessionFile, addSessionTool},
	} {
		if cfg.file == "" {
			continue
		}
		if !filepath.IsAbs(cfg.file) {
			return errors.New(liveUsage)
		}
		proposalToken, err := mcpclient.ReadToken(cfg.file)
		if err != nil {
			return err
		}
		if usedTokens[proposalToken] {
			return errors.New("cada tipo de consulta o propuesta requiere su propia clave")
		}
		proposals, err := mcpclient.NewProposalClient(*origin, proposalToken)
		if err != nil {
			return err
		}
		usedTokens[proposalToken] = true
		cfg.add(server, proposals)
	}
	if *proposalFile != "" || *wikiEditProposalFile != "" {
		maxLine = 128 * 1024
	}
	return server.Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(in), Writer: quietWriter{out}, MaxLineLength: maxLine})
}
