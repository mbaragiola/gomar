package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type adminOutletHistoryRow struct {
	Actor, Action, Timestamp, DateDisplay string
}

type adminOutletHistoryPage struct {
	DetailPath                    string
	Rows                          []adminOutletHistoryRow
	Number, Previous, Next, Total int
}

func adminCatalogHistoryID(path string) (string, int64, bool) {
	for _, list := range []string{adminOutletPath, adminSupplierPath, adminLegalEntityPath} {
		raw, prefix := strings.CutPrefix(path, list)
		if !prefix {
			continue
		}
		raw, suffix := strings.CutSuffix(raw, "/history/")
		if !suffix || raw == "" || strings.Trim(raw, "0123456789") != "" {
			return "", 0, false
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		return list, id, err == nil
	}
	return "", 0, false
}

func outletHistoryAction(name, active bool) string {
	switch {
	case name && active:
		return "Modificación de Nombre y Activo."
	case name:
		return "Modificación de Nombre."
	case active:
		return "Modificación de Activo."
	default:
		return "No ha modificado ningún campo."
	}
}

func (a *app) serveAdminCatalogHistory(w http.ResponseWriter, r *http.Request, page adminPage, list string, id int64) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	var object string
	itemQuery := `SELECT e.name || ' · ' || o.name FROM outlets o JOIN entities e ON e.id=o.entity_id WHERE o.id=$1`
	countQuery := "SELECT COUNT(*) FROM admin_outlet_history WHERE outlet_id=$1"
	rowsQuery := `SELECT u.username,h.occurred_at,h.name_changed,h.active_changed,h.action
		FROM admin_outlet_history h JOIN users u ON u.id=h.actor_id WHERE h.outlet_id=$1
		ORDER BY h.occurred_at,h.id LIMIT $2 OFFSET $3`
	if list == adminSupplierPath {
		itemQuery = "SELECT name FROM suppliers WHERE id=$1"
		countQuery = "SELECT COUNT(*) FROM admin_supplier_history WHERE supplier_id=$1"
		rowsQuery = `SELECT u.username,h.occurred_at,h.name_changed,h.active_changed,h.action
			FROM admin_supplier_history h JOIN users u ON u.id=h.actor_id WHERE h.supplier_id=$1
			ORDER BY h.occurred_at,h.id LIMIT $2 OFFSET $3`
	}
	if list == adminLegalEntityPath {
		itemQuery = "SELECT name FROM entities WHERE id=$1"
		countQuery = "SELECT COUNT(*) FROM admin_legal_entity_history WHERE entity_id=$1"
		rowsQuery = `SELECT u.username,h.occurred_at,FALSE,FALSE,'add' FROM admin_legal_entity_history h
			JOIN users u ON u.id=h.actor_id WHERE h.entity_id=$1 ORDER BY h.occurred_at,h.id LIMIT $2 OFFSET $3`
	}
	err := a.db.QueryRowContext(ctx, itemQuery, id).Scan(&object)
	if errors.Is(err, sql.ErrNoRows) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	history := adminOutletHistoryPage{Number: 1, DetailPath: list + strconv.FormatInt(id, 10) + "/change/"}
	if err := a.db.QueryRowContext(ctx, countQuery, id).Scan(&history.Total); err != nil {
		failure(w, err)
		return
	}
	pageCount := max(1, (history.Total+adminCatalogPageSize-1)/adminCatalogPageSize)
	if number, err := strconv.Atoi(r.URL.Query().Get("p")); err == nil {
		history.Number = number
		if number < 1 || number > pageCount {
			history.Number = pageCount
		}
	}
	if history.Number > 1 {
		history.Previous = history.Number - 1
	}
	if history.Number < pageCount {
		history.Next = history.Number + 1
	}
	rows, err := a.db.QueryContext(ctx, rowsQuery, id, adminCatalogPageSize, (history.Number-1)*adminCatalogPageSize)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var row adminOutletHistoryRow
		var name, active bool
		var action string
		if err := rows.Scan(&row.Actor, &row.Timestamp, &name, &active, &action); err != nil {
			failure(w, err)
			return
		}
		row.Action = outletHistoryAction(name, active)
		if list == adminSupplierPath && name {
			row.Action = "Modificación de Razón social."
			if active {
				row.Action = "Modificación de Razón social y Activo."
			}
		}
		if action == "add" {
			row.Action = "Agregado."
		}
		row.DateDisplay = row.Timestamp
		if moment, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil {
			row.DateDisplay = moment.In(payablesLocation).Format("02/01/2006 15:04")
		}
		history.Rows = append(history.Rows, row)
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	page.Title = "Historial de cambios: " + object
	page.OutletHistory = &history
	renderAdmin(w, page)
}
