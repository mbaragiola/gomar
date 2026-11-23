package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBookingReadToolsSendOnlyFixedTypedRequests(t *testing.T) {
	const bearer = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var requests atomic.Int32
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+bearer || r.Header.Get("Cookie") != "" {
			t.Error("unscoped http request")
		}
		w.Header().Set("Content-Type", "application/json")
		const base = "/api/v1/mcp/entities/7/outlets/8"
		switch r.URL.Path {
		case base + "/reservation-status":
			if r.URL.RawQuery != "date=2030-11-20" {
				t.Error(r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"date":"2030-11-20","timezone":"America/Argentina/Buenos_Aires","counts":[{"status":"pending","request_count":1,"party_size_total":4},{"status":"confirmed","request_count":0,"party_size_total":0},{"status":"cancelled","request_count":0,"party_size_total":0}],"guest":"not-for-chat"}`)
		case base + "/reservation-start-setup":
			switch r.URL.Query().Get("start_at") {
			case "2030-11-21T00:00:00Z":
				_, _ = io.WriteString(w, `{"start_configured":false,"guest":"not-for-chat"}`)
			case "2030-11-21T01:30:00Z":
				_, _ = io.WriteString(w, `{"start_configured":true}`)
			case "2030-11-21T03:00:00Z":
				_, _ = io.WriteString(w, `{"start_configured":null}`)
			default:
				t.Error("unexpected start setup query")
			}
		case base + "/reservation-table-setup":
			switch r.URL.RawQuery {
			case "table_number=7":
				_, _ = io.WriteString(w, `{"table_configured":false,"label":"not-for-chat"}`)
			case "table_number=8":
				_, _ = io.WriteString(w, `{"table_configured":true,"capacity":12}`)
			case "table_number=9":
				_, _ = io.WriteString(w, `{"table_configured":true}`)
			case "table_number=10":
				_, _ = io.WriteString(w, `{"table_configured":false,"capacity":4}`)
			default:
				t.Error("unexpected table setup query")
			}
		case base + "/published-menu":
			_, _ = io.WriteString(w, `{"published":true,"filename":"menu.pdf","size_bytes":100,"updated_at":"2030-11-20T12:00:00Z","url":"not-for-chat"}`)
		case base + "/pos-catalog":
			if r.URL.RawQuery != "after_product_id=1" {
				t.Error(r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"currency":"ARS","products":[{"id":2,"name":"Sandwich","price_minor":350,"revision":1,"active":true}],"actor":"not-for-chat"}`)
		case base + "/pos-catalog/2":
			_, _ = io.WriteString(w, `{"id":2,"name":"Sandwich","price_minor":350,"revision":2,"active":false,"currency":"ARS","cost":"not-for-chat"}`)
		case base + "/stock/products/2/movements-summary":
			if r.URL.RawQuery != "days=2&start_date=2030-11-20" {
				t.Error(r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"product_id":2,"timezone":"America/Argentina/Buenos_Aires","start_date":"2030-11-20","end_date_exclusive":"2030-11-22","kinds":[{"kind":"recepcion","movement_count":2,"net_units":5},{"kind":"merma","movement_count":1,"net_units":-1},{"kind":"conteo","movement_count":0,"net_units":0},{"kind":"venta","movement_count":0,"net_units":0}],"staff":"not-for-chat"}`)
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, "not-for-chat")
		}
	}))
	defer app.Close()
	native, err := mcpclient.New(app.URL, bearer)
	if err != nil {
		t.Fatal(err)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "booking-qa", Version: "1"}, nil)
	addBookingReadTools(s, native)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "qa", Version: "1"}, nil)
	session, err := c.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil || len(list.Tools) != 8 {
		t.Fatal(list, err)
	}
	for _, tool := range list.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal(tool.Name)
		}
	}
	for _, tc := range []struct {
		name, want string
		args       map[string]any
		denied     bool
	}{
		{"gomar_reservation_status", `"party_size_total":4`, map[string]any{"date": "2030-11-20"}, false},
		{"gomar_read_reservation_start_setup", `"start_configured":false`, map[string]any{"start_at": "2030-11-21T00:00:00Z"}, false},
		{"gomar_read_reservation_start_setup", `"start_configured":true`, map[string]any{"start_at": "2030-11-21T01:30:00Z"}, false},
		{"gomar_read_reservation_start_setup", "inválida", map[string]any{"start_at": "2030-11-21T03:00:00Z"}, true},
		{"gomar_read_reservation_start_setup", "UTC", map[string]any{"start_at": "2030-11-20T21:00:00-03:00"}, true},
		{"gomar_read_reservation_start_setup", "", nil, true},
		{"gomar_read_reservation_table_setup", `"table_configured":false`, map[string]any{"table_number": 7}, false},
		{"gomar_read_reservation_table_setup", `"capacity":12`, map[string]any{"table_number": 8}, false},
		{"gomar_read_reservation_table_setup", `"table_configured":true`, map[string]any{"table_number": 9}, false},
		{"gomar_read_reservation_table_setup", "inválida", map[string]any{"table_number": 10}, true},
		{"gomar_read_reservation_table_setup", "", map[string]any{"table_number": 0}, true},
		{"gomar_read_reservation_table_setup", "", map[string]any{"table_number": "07"}, true},
		{"gomar_read_reservation_table_setup", "", nil, true},
		{"gomar_published_menu", `"filename":"menu.pdf"`, nil, false},
		{"gomar_pos_catalog", `"price_minor":350`, map[string]any{"after_product_id": 1}, false},
		{"gomar_read_pos_product", `"active":false`, map[string]any{"product_id": 2}, false},
		{"gomar_read_pos_product", "permisos", map[string]any{"product_id": 2, "outlet_id": 9}, true},
		{"gomar_stock_movements", `"net_units":-1`, map[string]any{"product_id": 2, "start_date": "2030-11-20", "days": 2}, false},
		{"gomar_stock_movements", "31", map[string]any{"product_id": 2, "start_date": "2030-11-20", "days": 32}, true},
		{"gomar_published_menu", "permisos", map[string]any{"outlet_id": 9}, true},
	} {
		args := map[string]any{"entity_id": 7, "outlet_id": 8}
		for k, v := range tc.args {
			args[k] = v
		}
		got, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: args})
		if err != nil {
			t.Fatal(tc.name, err)
		}
		if got == nil || len(got.Content) != 1 {
			t.Fatal(tc.name, "bad output")
		}
		msg, ok := got.Content[0].(*mcp.TextContent)
		raw, _ := json.Marshal(got)
		if !ok || !strings.Contains(msg.Text, tc.want) || got.IsError != tc.denied ||
			strings.Contains(string(raw), "not-for-chat") || strings.Contains(string(raw), bearer) {
			t.Fatalf("%s: incorrect or excessive result: %s", tc.name, raw)
		}
	}
	if requests.Load() != 14 {
		t.Fatalf("want exactly fourteen fixed HTTP requests; got %d", requests.Load())
	}
}
