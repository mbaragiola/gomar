package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const documentAuditAdminPath = "/admin/purchases/auditevent/"
const documentAuditPageSize = 100

type adminAuditRow struct {
	ID                                                      int64
	Document, Action, Actor, Timestamp, DateDisplay, Reason string
}

type adminAuditPage struct {
	Rows                   []adminAuditRow
	Number, Previous, Next int
}

const selectAdminAudit = `
	SELECT a.id,d.document_type,d.letter,d.point_of_sale,d.number,a.action,COALESCE(e.handle,u.username),a.occurred_at,a.reason
	FROM document_audit a JOIN documents d ON d.id=a.document_id JOIN users u ON u.id=a.actor_id
	LEFT JOIN organization_employees e ON e.user_id=u.id AND e.entity_id=d.entity_id`

func scanAdminAudit(scanner interface{ Scan(...any) error }) (adminAuditRow, error) {
	var row adminAuditRow
	var kind, letter, action string
	var point, document int64
	err := scanner.Scan(&row.ID, &kind, &letter, &point, &document, &action, &row.Actor, &row.Timestamp, &row.Reason)
	if err != nil {
		return row, err
	}
	name := documentTypes[kind]
	if letter != "" {
		name += " " + letter
	}
	row.Document = fmt.Sprintf("%s %05d-%08d", name, point, document)
	row.Action = documentAuditLabels[action]
	row.DateDisplay = row.Timestamp
	if moment, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil {
		row.DateDisplay = moment.In(payablesLocation).Format("02/01/2006 15:04")
	}
	return row, nil
}

func adminAuditDetailID(path string) (int64, bool) {
	rest, prefix := strings.CutPrefix(path, documentAuditAdminPath)
	key, suffix := strings.CutSuffix(rest, "/change/")
	if !prefix || !suffix || key == "" || strings.ContainsRune(key, '/') {
		return 0, false
	}
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return 0, true
	}
	return id, true
}

func (a *app) serveAdminAuditDetail(w http.ResponseWriter, r *http.Request, page adminPage, id int64) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		w.WriteHeader(http.StatusForbidden)
		return
	default:
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if id < 1 {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	row, err := scanAdminAudit(a.db.QueryRowContext(r.Context(), selectAdminAudit+" WHERE a.id=$1", id))
	if errors.Is(err, sql.ErrNoRows) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	page.Title = "Evento de auditoría"
	page.AuditDetail = &row
	renderAdmin(w, page)
}

func (a *app) serveAdminAudit(w http.ResponseWriter, r *http.Request, page adminPage) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	number := 1
	if parsed, err := strconv.Atoi(r.URL.Query().Get("p")); err == nil {
		number = parsed
	}
	if number < 1 || number > int(^uint(0)>>1)/documentAuditPageSize {
		http.Redirect(w, r, "?e=1", http.StatusFound)
		return
	}
	// PostgreSQL timestamps have microsecond precision. Compare whole UTC
	// seconds first and the original fractional digits separately.
	const order = `
		ORDER BY CAST(substr(a.occurred_at,1,19) ||
			CASE WHEN right(a.occurred_at,1)='Z' THEN 'Z' ELSE right(a.occurred_at,6) END AS TIMESTAMPTZ),
			CASE WHEN substr(a.occurred_at,20,1)='.' THEN
				rpad(substr(a.occurred_at,21,length(a.occurred_at)-20-
					CASE WHEN right(a.occurred_at,1)='Z' THEN 1 ELSE 6 END),9,'0')
			ELSE '000000000' END,a.id LIMIT $1 OFFSET $2`
	rows, err := a.db.QueryContext(r.Context(), selectAdminAudit+order,
		documentAuditPageSize+1, (number-1)*documentAuditPageSize)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	result := adminAuditPage{Number: number, Rows: []adminAuditRow{}}
	if number > 1 {
		result.Previous = number - 1
	}
	for rows.Next() {
		if len(result.Rows) == documentAuditPageSize {
			result.Next = number + 1
			break
		}
		row, err := scanAdminAudit(rows)
		if err != nil {
			failure(w, err)
			return
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	if number != 1 && len(result.Rows) == 0 {
		http.Redirect(w, r, "?e=1", http.StatusFound)
		return
	}
	page.Title = "Eventos de auditoría"
	page.Audit = &result
	renderAdmin(w, page)
}
