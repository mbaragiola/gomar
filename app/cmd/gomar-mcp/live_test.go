package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLiveStdioScopedTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const secret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var requests atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Method != "GET" || r.Header.Get("Cookie") != "" {
			t.Error("invalid HTTP request")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/mcp/me":
			_, _ = io.WriteString(w, `{"id":7,"username":"prueba","unexpected":"`+secret+`"}`)
		case "/api/v1/mcp/entities":
			_, _ = io.WriteString(w, `{"entities":[{"id":20,"name":"Ficción"}]}`)
		case "/api/v1/mcp/entities/20/outlets":
			_, _ = io.WriteString(w, `{"outlets":[{"id":10,"name":"Centro"}]}`)
		case "/api/v1/mcp/entities/20/sales-summary":
			if r.URL.RawQuery != "outlet_id=10&period=this_month" {
				t.Errorf("incorrect sales query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"period":"this_month","timezone":"America/Argentina/Buenos_Aires","from":"2026-11-01T00:00:00-03:00","to":"2026-12-01T00:00:00-03:00","currency":"ARS","sale_count":1,"total_minor":1050,"outlets":[{"id":10,"name":"Centro","sale_count":1,"total_minor":1050}]}`)
		case "/api/v1/mcp/entities/20/outlets/10/knowledge":
			if r.URL.Query().Get("q") != "turno & cocina" || len(r.URL.Query()) != 1 {
				t.Errorf("incorrect knowledge query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"articles":[{"id":5,"title":"Turno","version":1,"updated_at":"2026-11-01T03:00:00Z","body":"`+secret+`"}],"truncated":false}`)
		case "/api/v1/mcp/entities/20/outlets/10/knowledge/5":
			_, _ = io.WriteString(w, `{"id":5,"title":"Turno","version":1,"updated_at":"2026-11-01T03:00:00Z","body":"Texto de prueba"}`)
		case "/api/v1/mcp/entities/20/outlets/10/stock-alert-summary":
			if r.URL.RawQuery != "" {
				t.Error("unexpected stock alert summary filter")
			}
			_, _ = io.WriteString(w, `{"enabled":true,"tracked":36,"negative":1,"at_or_below_minimum":2,"healthy":33,"name":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/stock/2":
			_, _ = io.WriteString(w, `{"id":2,"outlet_enabled":true,"product_enabled":true,"minimum":8,"balance":7,"alert":"low","name":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/11/stock/2", "/api/v1/mcp/entities/21/outlets/10/stock/2":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, secret)
		case "/api/v1/mcp/entities/20/outlets/10/stock":
			switch r.URL.RawQuery {
			case "after_product_id=1&alert=low":
				_, _ = io.WriteString(w, `{"enabled":true,"alert_filter":"low","products":[{"id":2,"name":"Pan","enabled":true,"minimum":8,"balance":8,"alert":"low"}]}`)
			case "alert=negative":
				// Simulate an older server silently ignoring the filter.
				_, _ = io.WriteString(w, `{"enabled":true,"products":[]}`)
			case "alert=any":
				// Acknowledging a filter is not enough if returned rows do not match.
				_, _ = io.WriteString(w, `{"enabled":true,"alert_filter":"any","products":[{"id":2,"name":"Pan","enabled":false,"minimum":8,"balance":-1,"alert":"negative"}]}`)
			case "after_product_id=2&alert=low":
				_, _ = io.WriteString(w, `{"enabled":true,"alert_filter":"low","products":[{"id":3,"name":"Pan","enabled":true,"minimum":0,"alert":"low"}]}`)
			case "after_product_id=3&alert=low":
				_, _ = io.WriteString(w, `{"enabled":true,"alert_filter":"low","products":[{"id":4,"name":"Pan","enabled":true,"minimum":null,"balance":0,"alert":"low"}]}`)
			case "after_product_id=4&alert=low":
				_, _ = io.WriteString(w, `{"enabled":null,"alert_filter":"low","products":[]}`)
			case "after_product_id=5&alert=low":
				_, _ = io.WriteString(w, `{"enabled":true,"alert_filter":"low","products":[{"id":6,"name":"Pan","enabled":null,"minimum":0,"balance":0,"alert":"low"}]}`)
			default:
				if r.URL.RawQuery != "after_product_id=1" {
					t.Errorf("incorrect stock query: %s", r.URL.RawQuery)
				}
				_, _ = io.WriteString(w, `{"enabled":true,"products":[{"id":2,"name":"Pan","enabled":true,"minimum":8,"balance":7,"alert":"low"}]}`)
			}
		case "/api/v1/mcp/entities/20/outlets/10/delivery-status-summary":
			if r.URL.RawQuery != "" {
				t.Error("unexpected delivery summary filter")
			}
			_, _ = io.WriteString(w, `{"window_limit":100,"received":2,"accepted":3,"cancelled":1,"item":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/delivery-orders":
			_, _ = io.WriteString(w, `{"orders":[{"id":10,"provider":"rappi","status":"received","received_at":"2026-11-01T03:00:00Z","updated_at":"2026-11-01T03:00:00Z","external_order_id":"`+secret+`"}],"truncated":false}`)
		case "/api/v1/mcp/entities/20/outlets/10/payables-summary":
			_, _ = io.WriteString(w, `{"as_of":"2026-11-01","totals":[{"currency":"USD","outstanding_count":1,"outstanding_minor":55,"overdue_count":0,"overdue_minor":0}],"supplier":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/document-totals":
			if r.URL.RawQuery != "" {
				t.Error("unexpected document totals query")
			}
			_, _ = io.WriteString(w, `{"count":1,"totals":[{"currency":"ARS","count":1,"total_minor":"-100","issuer":"`+secret+`"}]}`)
		case "/api/v1/mcp/entities/20/outlets/10/documents/7/payable-preflight":
			if r.URL.RawQuery != "" {
				t.Error("unexpected preflight query")
			}
			_, _ = io.WriteString(w, `{"eligible":false,"blocker":"not_reviewed","private":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/documents/7":
			_, _ = io.WriteString(w, `{"id":7,"revision":0,"status":"draft","type":"invoice","reference":"Factura 00001-00000007","issue_date":"2026-11-01","currency":"ARS","total_minor":0,"private":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/reconciliation-runs":
			if r.URL.RawQuery != "after_id=5" {
				t.Error("unexpected reconciliation cursor")
			}
			_, _ = io.WriteString(w, `{"runs":[{"id":8,"revision":1,"status":"draft","created_at":"2026-11-01T03:00:00Z","counts":{"outlets":1,"sources":0,"rows":0,"confirmed":0,"rejected":0}}]}`)
		case "/api/v1/mcp/entities/20/outlets/10/reconciliation-runs/8/close-eligibility":
			if r.URL.RawQuery != "" {
				t.Error("unexpected reconciliation close eligibility query")
			}
			_, _ = io.WriteString(w, `{"eligible":false,"blocker":"unsupported_review_data","bank":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/reconciliation-runs/8/setup":
			_, _ = io.WriteString(w, `{"id":8,"revision":2,"status":"draft","match_reference":true,"has_payments_source":true,"has_cash_flow_source":false,"reference_mapping_incomplete":true,"unsupported_source_setup":false,"over_row_limit":false}`)
		case "/api/v1/mcp/entities/20/outlets/10/reconciliation-runs/8":
			_, _ = io.WriteString(w, `{"id":8,"revision":1,"status":"draft","created_at":"2026-11-01T03:00:00Z","counts":{"outlets":1,"sources":1,"rows":5,"confirmed":0,"rejected":0},"private":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/payable-records":
			switch r.URL.RawQuery {
			case "after_id=4":
				_, _ = io.WriteString(w, `{"records":[{"id":5,"status":"partial","currency":"ARS","issue_date":"2026-11-01","posted_date":"2026-11-02","total_minor":100,"balance_minor":50}]}`)
			case "after_id=4&due_from=2026-11-01&due_to=2026-11-08":
				_, _ = io.WriteString(w, `{"records":[{"id":5,"status":"partial","currency":"ARS","issue_date":"2026-11-01","due_date":"2026-11-08","posted_date":"2026-11-02","total_minor":100,"balance_minor":50}]}`)
			case "due_from=2026-12-01&due_to=2026-12-05":
				_, _ = io.WriteString(w, `{"records":[{"id":5,"status":"partial","currency":"ARS","issue_date":"2026-11-01","due_date":"2026-12-06","posted_date":"2026-11-02","total_minor":100,"balance_minor":50}]}`)
			default:
				t.Error("unexpected payable query")
			}
		case "/api/v1/mcp/entities/20/outlets/10/payable-records/5":
			_, _ = io.WriteString(w, `{"id":5,"status":"partial","currency":"ARS","issue_date":"2026-11-01","posted_date":"2026-11-02","total_minor":100,"balance_minor":50,"supplier":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/payable-records/5/movements":
			if r.URL.RawQuery != "cursor=5.2" {
				t.Error("unexpected payable history cursor")
			}
			_, _ = io.WriteString(w, `{"payable_id":5,"currency":"ARS","movements":[{"kind":"payment","date":"2026-11-03","delta_minor":-50,"balance_minor":50,"actor":"`+secret+`","payment_id":"`+secret+`","reason":"`+secret+`"}]}`)
		case "/api/v1/mcp/entities/20/outlets/10/recorded-payments":
			_, _ = io.WriteString(w, `{"records":[{"id":12,"status":"draft","currency":"USD","created_date":"2026-11-03","total_minor":100}]}`)
		case "/api/v1/mcp/entities/20/outlets/10/recorded-payments/12":
			_, _ = io.WriteString(w, `{"id":12,"status":"draft","currency":"USD","created_date":"2026-11-03","total_minor":100,"reference":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/stock/products/3/movements":
			if r.URL.RawQuery != "after_id=2&days=1&start_date=2026-11-01" {
				t.Error("unexpected stock detail cursor and dates")
			}
			_, _ = io.WriteString(w, `{"timezone":"America/Argentina/Buenos_Aires","start_date":"2026-11-01","end_date_exclusive":"2026-11-02","movements":[{"kind":"merma","delta":-5,"recorded_at":"2026-11-01T00:00:01-03:00","actor":"`+secret+`"}]}`)
		case "/api/v1/mcp/entities/20/outlets/10/reservation-availability":
			if r.URL.RawQuery != "date=2026-11-01&party_size=6" {
				t.Error("unexpected reservation date or party size")
			}
			_, _ = io.WriteString(w, `{"date":"2026-11-01","timezone":"America/Argentina/Buenos_Aires","party_size":6,"slots":[{"start_at":"2026-11-01T21:00:00-03:00","table_label":"`+secret+`"}]}`)
		case "/api/v1/mcp/entities/20/outlets/10/reservation-setup":
			if r.URL.RawQuery != "" {
				t.Error("unexpected reservation setup query")
			}
			_, _ = io.WriteString(w, `{"venue_configured":true,"venue_enabled":false,"public_feature_enabled":true,"timezone":"America/Argentina/Buenos_Aires","contact":"`+secret+`"}`)
		case "/api/v1/mcp/entities/20/outlets/10/reservation-requests":
			if r.URL.RawQuery != "after_id=00000000000000000000000000000003&date=2026-11-01" {
				t.Error("unexpected reservation cursor")
			}
			_, _ = io.WriteString(w, `{"date":"2026-11-01","timezone":"America/Argentina/Buenos_Aires","requests":[{"id":"00000000000000000000000000000007","status":"pending","party_size":2,"start_at":"2026-11-01T00:00:01-03:00","guest_name":"`+secret+`"}]}`)
		case "/api/v1/mcp/entities/20/outlets/10/reservation-requests/00000000000000000000000000000007":
			if r.URL.RawQuery != "date=2026-11-01" {
				t.Error("unexpected reservation date")
			}
			_, _ = io.WriteString(w, `{"id":"00000000000000000000000000000007","status":"confirmed","party_size":2,"start_at":"2026-11-01T00:00:01-03:00","table_label":"`+secret+`"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, secret)
		}
	}))
	defer web.Close()
	path := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(path, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	uploadReader, uploadWriter := io.Pipe()
	downloadReader, downloadWriter := io.Pipe()
	defer uploadReader.Close()
	defer uploadWriter.Close()
	defer downloadReader.Close()
	defer downloadWriter.Close()
	finished := make(chan error, 1)
	go func() {
		defer downloadWriter.Close()
		finished <- runLive(ctx, []string{"--origin", web.URL, "--token-file", path}, uploadReader, downloadWriter)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "qa", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: downloadReader, Writer: uploadWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	metadata, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil || len(metadata.Tools) != 40 {
		t.Fatal(metadata, err)
	}
	for _, tool := range metadata.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %s isn't strictly read-only", tool.Name)
		}
	}
	for _, test := range []struct {
		name    string
		args    map[string]any
		want    string
		isError bool
	}{
		{"gomar_current_user", nil, `"username":"prueba"`, false},
		{"gomar_list_entities", nil, `"id":20`, false},
		{"gomar_list_outlets", map[string]any{"entity_id": 20}, `"name":"Centro"`, false},
		{"gomar_sales_summary", map[string]any{"entity_id": 20, "outlet_id": 10, "period": "this_month"}, `"total_minor":1050`, false},
		{"gomar_sales_summary", map[string]any{"entity_id": 21, "period": "this_month"}, "permisos", true},
		{"gomar_search_knowledge", map[string]any{"entity_id": 20, "outlet_id": 10, "query": "turno & cocina"}, `"title":"Turno"`, false},
		{"gomar_read_knowledge_article", map[string]any{"entity_id": 20, "outlet_id": 10, "article_id": 5}, `"body":"Texto de prueba"`, false},
		{"gomar_stock_alert_summary", map[string]any{"entity_id": 20, "outlet_id": 10}, `"tracked":36`, false},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "after_product_id": 1}, `"balance":7`, false},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "after_product_id": 1, "alert": "low"}, `"alert_filter":"low"`, false},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "alert": "negative"}, "", true},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "alert": "any"}, "", true},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "alert": ""}, "alert", true},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "after_product_id": 2, "alert": "low"}, "", true},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "after_product_id": 3, "alert": "low"}, "", true},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "after_product_id": 4, "alert": "low"}, "", true},
		{"gomar_list_stock", map[string]any{"entity_id": 20, "outlet_id": 10, "after_product_id": 5, "alert": "low"}, "", true},
		{"gomar_read_stock_product", map[string]any{"entity_id": 20, "outlet_id": 10, "product_id": 2}, `"alert":"low"`, false},
		{"gomar_read_stock_product", map[string]any{"entity_id": 20, "outlet_id": 11, "product_id": 2}, "permisos", true},
		{"gomar_read_stock_product", map[string]any{"entity_id": 21, "outlet_id": 10, "product_id": 2}, "permisos", true},
		{"gomar_delivery_status_summary", map[string]any{"entity_id": 20, "outlet_id": 10}, `"accepted":3`, false},
		{"gomar_list_recent_delivery", map[string]any{"entity_id": 20, "outlet_id": 10}, `"status":"received"`, false},
		{"gomar_payables_summary", map[string]any{"entity_id": 20, "outlet_id": 10}, `"outstanding_minor":55`, false},
		{"gomar_payables_summary", map[string]any{"entity_id": 20, "outlet_id": 11}, "permisos", true},
		{"gomar_list_stock", map[string]any{"entity_id": 20}, "", true},
		{"gomar_document_totals", map[string]any{"entity_id": 20, "outlet_id": 10}, `"total_minor":"-100"`, false},
		{"gomar_document_totals", map[string]any{"entity_id": 20, "outlet_id": 10, "status": "reviewed"}, "", true},
		{"gomar_check_payable_eligibility", map[string]any{"entity_id": 20, "outlet_id": 10, "document_id": 7}, `"blocker":"not_reviewed"`, false},
		{"gomar_check_payable_eligibility", map[string]any{"entity_id": 20, "outlet_id": 11, "document_id": 7}, "permisos", true},
		{"gomar_read_document", map[string]any{"entity_id": 20, "outlet_id": 10, "document_id": 7}, `"revision":0`, false},
		{"gomar_read_document", map[string]any{"entity_id": 20, "outlet_id": 10, "document_id": 0}, "document_id", true},
		{"gomar_list_reconciliation_runs", map[string]any{"entity_id": 20, "outlet_id": 10, "after_id": 5}, `"id":8`, false},
		{"gomar_read_reconciliation_setup", map[string]any{"entity_id": 20, "outlet_id": 10, "run_id": 8}, `"has_cash_flow_source":false`, false},
		{"gomar_check_reconciliation_close_eligibility", map[string]any{"entity_id": 20, "outlet_id": 10, "run_id": 8}, `"blocker":"unsupported_review_data"`, false},
		{"gomar_check_reconciliation_close_eligibility", map[string]any{"entity_id": 20, "outlet_id": 11, "run_id": 8}, "permisos", true},
		{"gomar_read_reconciliation_run", map[string]any{"entity_id": 20, "outlet_id": 10, "run_id": 8}, `"rows":5`, false},
		{"gomar_read_reconciliation_run", map[string]any{"entity_id": 20, "outlet_id": 11, "run_id": 8}, "permisos", true},
		{"gomar_list_payables", map[string]any{"entity_id": 20, "outlet_id": 10, "after_id": 4}, `"balance_minor":50`, false},
		{"gomar_list_payables", map[string]any{"entity_id": 20, "outlet_id": 10, "after_id": 4, "due_from": "2026-11-01", "due_to": "2026-11-08"}, `"due_date":"2026-11-08"`, false},
		{"gomar_list_payables", map[string]any{"entity_id": 20, "outlet_id": 10, "due_from": "2026-11-01"}, "due_from y due_to", true},
		{"gomar_list_payables", map[string]any{"entity_id": 20, "outlet_id": 10, "due_to": "2026-11-08"}, "due_from y due_to", true},
		{"gomar_list_payables", map[string]any{"entity_id": 20, "outlet_id": 10, "due_from": nil, "due_to": nil}, "", true},
		{"gomar_list_payables", map[string]any{"entity_id": 20, "outlet_id": 10, "due_from": "2026-11-08", "due_to": "2026-11-01"}, "posterior", true},
		{"gomar_list_payables", map[string]any{"entity_id": 20, "outlet_id": 10, "due_from": "2026-12-01", "due_to": "2026-12-05"}, "", true},
		{"gomar_read_payable", map[string]any{"entity_id": 20, "outlet_id": 10, "payable_id": 5}, `"status":"partial"`, false},
		{"gomar_list_payable_movements", map[string]any{"entity_id": 20, "outlet_id": 10, "payable_id": 5, "cursor": "5.2"}, `"delta_minor":-50`, false},
		{"gomar_list_payable_movements", map[string]any{"entity_id": 20, "outlet_id": 10, "payable_id": 5, "cursor": "4.2"}, "misma cuenta", true},
		{"gomar_list_recorded_payments", map[string]any{"entity_id": 20, "outlet_id": 10}, `"status":"draft"`, false},
		{"gomar_read_recorded_payment", map[string]any{"entity_id": 20, "outlet_id": 10, "payment_id": 12}, `"total_minor":100`, false},
		{"gomar_list_stock_movements", map[string]any{"entity_id": 20, "outlet_id": 10, "product_id": 3, "days": 1, "start_date": "2026-11-01", "after_id": 2}, `"delta":-5`, false},
		{"gomar_read_recorded_payment", map[string]any{"entity_id": 20, "outlet_id": 11, "payment_id": 12}, "permisos", true},
		{"gomar_read_reservation_setup", map[string]any{"entity_id": 20, "outlet_id": 10}, `"venue_enabled":false`, false},
		{"gomar_list_reservation_requests", map[string]any{"entity_id": 20, "outlet_id": 10, "date": "2026-11-01", "after_id": "00000000000000000000000000000003"}, `"party_size":2`, false},
		{"gomar_reservation_availability", map[string]any{"entity_id": 20, "outlet_id": 10, "date": "2026-11-01", "party_size": 6}, `"slots":[{"start_at":"2026-11-01T21:00:00-03:00"}]`, false},
		{"gomar_reservation_availability", map[string]any{"entity_id": 20, "outlet_id": 10, "date": "2026-11-01", "party_size": 0}, "1 a 12", true},
		{"gomar_read_reservation_request", map[string]any{"entity_id": 20, "outlet_id": 10, "date": "2026-11-01", "reservation_id": "00000000000000000000000000000007"}, `"status":"confirmed"`, false},
		{"gomar_read_reservation_request", map[string]any{"entity_id": 20, "outlet_id": 11, "date": "2026-11-01", "reservation_id": "00000000000000000000000000000007"}, "permisos", true},
		{"gomar_read_reservation_request", map[string]any{"entity_id": 20, "outlet_id": 10, "date": "2026-11-01", "reservation_id": "x/path"}, "reservation_id", true},
		{"gomar_sales_summary", map[string]any{"entity_id": 20, "period": "this_month", "path": "http://forged.invalid"}, "", true},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil {
			t.Fatal(test.name, err)
		}
		if len(result.Content) != 1 {
			t.Fatal(test.name, result)
		}
		block, ok := result.Content[0].(*mcp.TextContent)
		wire, _ := json.Marshal(result)
		if !ok || result.IsError != test.isError || !strings.Contains(block.Text, test.want) || strings.Contains(string(wire), secret) ||
			(!test.isError && result.StructuredContent == nil) {
			t.Fatalf("unexpected result for %s: %s", test.name, wire)
		}
	}
	if requests.Load() != 47 {
		t.Fatalf("wrong number of requests: %d", requests.Load())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
