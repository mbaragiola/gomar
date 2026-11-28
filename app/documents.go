package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mbaragiola/gomar/app/internal/documentref"
)

const documentScope = `
	FROM documents d JOIN outlets o ON o.id=d.outlet_id AND o.entity_id=d.entity_id
	JOIN outlet_grants g ON g.outlet_id=o.id JOIN suppliers s ON s.id=d.supplier_id AND s.entity_id=d.entity_id
	WHERE d.entity_id=$1 AND g.membership_id=$2 AND o.active AND d.deleted_at IS NULL
	AND (CAST($3 AS BIGINT) IS NULL OR d.outlet_id=$3)`

const selectDocuments = `SELECT d.id,o.id,o.name,s.id,s.name,d.issuer_cuit,d.document_type,d.letter,
	d.point_of_sale,d.number,d.issue_date,d.due_date,d.currency,d.subtotal_minor,d.tax_minor,d.total_minor,
	d.description,d.status,d.revision,d.created_at,d.updated_at,
	(SELECT p.id FROM ap_payables p WHERE p.source_document_id=d.id)` + documentScope

// Documents and AP share the entity lock. Scope first, then recheck the
// current session under the lock before repeating the domain read. Entity
// serialization prevents a concurrent review, delete or supplier change
// from racing the document snapshot used to post a payable.
func (a *app) lockDocumentWrite(r *http.Request, tx *sql.Tx, u user, entityID int64) error {
	if err := lockPostgresEntity(r.Context(), tx, entityID); err != nil {
		return err
	}
	current, err := currentUser(nil, r, tx)
	if err != nil {
		return err
	}
	if current.id == 0 || current.id != u.id {
		return unauthenticated
	}
	return nil
}

func isDocumentIdentityConflict(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23505" && pg.ConstraintName == "documents_identity_unique"
	}
	return false
}

// A remote COMMIT can be acknowledged by PostgreSQL but lost on the
// connection. Never unlink an owned image that a committed row may reference.
// A possible orphan is safer and must be reconciled before a strict backup.
func (a *app) retainAfterDocumentCommitError(err error) bool {
	if err == nil || errors.Is(err, pgx.ErrTxCommitRollback) || pgconn.SafeToRetry(err) {
		return false
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (strings.HasPrefix(pg.Code, "23") || strings.HasPrefix(pg.Code, "40")) {
		return false
	}
	return true
}

func documentOutlet(r *http.Request, tx *sql.Tx, m entityMembership) (sql.NullInt64, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return sql.NullInt64{}, badInput
	}
	values, supplied := query["outlet_id"]
	if !supplied {
		return sql.NullInt64{}, nil
	}
	id, err := strconv.ParseInt(values[len(values)-1], 10, 64)
	if err != nil {
		return sql.NullInt64{}, badInput
	}
	return sql.NullInt64{Int64: id, Valid: true}, requireSelectedDocumentOutlet(tx, m, id)
}

func requireSelectedDocumentOutlet(tx *sql.Tx, m entityMembership, id int64) error {
	var allowed bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
		WHERE g.membership_id=$1 AND o.entity_id=$2 AND o.id=$3 AND o.active=TRUE)`, m.id, m.ID, id).Scan(&allowed)
	if err == nil && !allowed {
		return missingOutlet
	}
	return err
}

type documentListItem struct {
	ID          int64    `json:"id"`
	Entity      int64    `json:"entity_id"`
	Outlet      outlet   `json:"outlet"`
	Supplier    supplier `json:"supplier"`
	Type        string   `json:"tipo"`
	TypeLabel   string   `json:"tipo_nombre"`
	Letter      string   `json:"letra"`
	PointOfSale int64    `json:"punto_venta"`
	Number      int64    `json:"numero"`
	Reference   string   `json:"referencia"`
	IssueDate   string   `json:"fecha_emision"`
	DueDate     *string  `json:"fecha_vencimiento"`
	Currency    string   `json:"moneda"`
	Subtotal    *string  `json:"neto"`
	Tax         *string  `json:"iva"`
	Total       string   `json:"total"`
	Description string   `json:"descripcion"`
	Status      string   `json:"estado"`
	StatusLabel string   `json:"estado_nombre"`
	Revision    int64    `json:"revision"`
	Created     string   `json:"creado"`
	Updated     string   `json:"actualizado"`
	PayableID   *int64   `json:"payable_id"`
}

var documentTypes = map[string]string{"invoice": "Factura", "credit_note": "Nota de crédito", "debit_note": "Nota de débito"}
var documentStates = map[string]string{"draft": "Pendiente", "reviewed": "Revisado"}

func minorString(amount *big.Int) string {
	digits := amount.String()
	prefix := ""
	if strings.HasPrefix(digits, "-") {
		prefix, digits = "-", digits[1:]
	}
	if len(digits) < 3 {
		digits = strings.Repeat("0", 3-len(digits)) + digits
	}
	point := len(digits) - 2
	return prefix + digits[:point] + "." + digits[point:]
}

func optionalMoney(amount sql.NullInt64) *string {
	if !amount.Valid {
		return nil
	}
	value := minorString(big.NewInt(amount.Int64))
	return &value
}

func scanDocument(row interface{ Scan(...any) error }, entityID int64) (documentListItem, error) {
	item := documentListItem{Entity: entityID}
	var subtotal, tax sql.NullInt64
	var total int64
	if err := row.Scan(&item.ID, &item.Outlet.ID, &item.Outlet.Name, &item.Supplier.ID, &item.Supplier.Name,
		&item.Supplier.CUIT, &item.Type, &item.Letter, &item.PointOfSale, &item.Number, &item.IssueDate,
		&item.DueDate, &item.Currency, &subtotal, &tax, &total, &item.Description, &item.Status, &item.Revision,
		&item.Created, &item.Updated, &item.PayableID); err != nil {
		return documentListItem{}, err
	}
	item.TypeLabel, item.StatusLabel = documentTypes[item.Type], documentStates[item.Status]
	item.Reference = fmt.Sprintf("%s%s %05d-%08d", item.TypeLabel, prefixDocumentLetter(item.Letter), item.PointOfSale, item.Number)
	item.Subtotal, item.Tax, item.Total = optionalMoney(subtotal), optionalMoney(tax), minorString(big.NewInt(total))
	return item, nil
}

func listDocuments(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	only, err := documentOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, nil, badInput
	}
	reference, validReference := documentref.TakeQuery(query)
	draftOnly, validStatus := documentref.TakeDraftStatus(query)
	if !validReference || !validStatus || draftOnly && reference != nil {
		return 0, nil, badInput
	}
	if reference != nil || draftOnly {
		if !only.Valid || len(query["outlet_id"]) != 1 {
			return 0, nil, badInput
		}
		query.Del("outlet_id")
		cursor, err := mcpAccountingCursorValues(query)
		if err != nil {
			return 0, nil, err
		}
		if draftOnly {
			return nativeDraftQueue(r, tx, m, only.Int64, cursor)
		}
		return nativePrintedReferenceMatches(r, tx, m, only.Int64, cursor, *reference)
	}
	rows, err := tx.Query(selectDocuments+
		" ORDER BY d.issue_date DESC,d.id DESC", m.ID, m.id, only)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	items := []documentListItem{}
	for rows.Next() {
		item, err := scanDocument(rows, m.ID)
		if err != nil {
			return 0, nil, err
		}
		items = append(items, item)
	}
	return 200, items, rows.Err()
}

type printedReferenceMatch struct {
	ID        int64  `json:"id"`
	IssueDate string `json:"issue_date"`
	Status    string `json:"status"`
}

// Native lookup lists only enough to select one row. Full reference
// information about a document still requires an explicit native detail read.
func nativePrintedReferenceMatches(r *http.Request, tx *sql.Tx, m entityMembership, outlet, cursor int64, reference documentref.Reference) (int, any, error) {
	rows, err := tx.QueryContext(r.Context(), `SELECT d.id, d.issue_date, d.status`+documentScope+
		` AND d.id>$4 AND d.document_type=$5 AND d.letter=$6 AND d.point_of_sale=$7 AND d.number=$8
		ORDER BY d.id LIMIT $9`,
		m.ID, m.id, outlet, cursor, reference.DocumentType, reference.Letter, reference.PointOfSale, reference.Number, mcpPageSize+1)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	result := struct {
		Matches   []printedReferenceMatch `json:"matches"`
		Reference documentref.Reference   `json:"printed_reference"`
		NextID    int64                   `json:"next_id,omitempty"`
	}{Matches: []printedReferenceMatch{}, Reference: reference}
	for rows.Next() {
		var row printedReferenceMatch
		if err := rows.Scan(&row.ID, &row.IssueDate, &row.Status); err != nil {
			return 0, nil, err
		}
		result.Matches = append(result.Matches, row)
	}
	if len(result.Matches) > mcpPageSize {
		result.Matches = result.Matches[:mcpPageSize]
		result.NextID = result.Matches[mcpPageSize-1].ID
	}
	return 200, result, rows.Err()
}

type draftQueueRow struct {
	ID        int64  `json:"id"`
	IssueDate string `json:"issue_date"`
	Type      string `json:"type"`
	Reference string `json:"reference,omitempty"`
}

func nativeDraftQueue(r *http.Request, tx *sql.Tx, m entityMembership, outlet, cursor int64) (int, any, error) {
	rows, err := tx.QueryContext(r.Context(), `SELECT d.id,d.issue_date,d.document_type,d.letter,d.point_of_sale,d.number`+documentScope+
		` AND d.id>$4 AND d.status='draft' ORDER BY d.id LIMIT $5`, m.ID, m.id, outlet, cursor, mcpPageSize+1)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	page := struct {
		Drafts       []draftQueueRow `json:"drafts"`
		StatusFilter string          `json:"status_filter"`
		NextID       int64           `json:"next_id,omitempty"`
	}{Drafts: []draftQueueRow{}, StatusFilter: documentref.DraftStatus}
	for rows.Next() {
		var row draftQueueRow
		var letter string
		var point, number int64
		if err := rows.Scan(&row.ID, &row.IssueDate, &row.Type, &letter, &point, &number); err != nil {
			return 0, nil, err
		}
		// Do not project noncanonical legacy letter text. The rest of the
		// draft remains selectable even if it has no recognized label.
		if letter == "" || len(letter) == 1 && letter[0] >= 'A' && letter[0] <= 'Z' {
			row.Reference = fmt.Sprintf("%s%s %05d-%08d", documentTypes[row.Type], prefixDocumentLetter(letter), point, number)
		}
		page.Drafts = append(page.Drafts, row)
	}
	if len(page.Drafts) > mcpPageSize {
		page.Drafts = page.Drafts[:mcpPageSize]
		page.NextID = page.Drafts[mcpPageSize-1].ID
	}
	return 200, page, rows.Err()
}

func prefixDocumentLetter(letter string) string {
	if letter == "" {
		return ""
	}
	return " " + letter
}

func documentDetail(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	id, err := pathID(r, "document")
	if err != nil {
		return 0, nil, err
	}
	item, err := readDocumentView(tx, m, id)
	return 200, item, err
}

// Only the filtered row click uses this read. An operator can legitimately
// have grants on two outlets, but must not land in the other outlet if a
// native edit moved the document after the list was loaded.
func (a *app) printedReferenceDocumentDetail(w http.ResponseWriter, r *http.Request, u user) error {
	return a.selectedDocumentDetail(w, r, u, false)
}

func (a *app) draftQueueDocumentDetail(w http.ResponseWriter, r *http.Request, u user) error {
	return a.selectedDocumentDetail(w, r, u, true)
}

func (a *app) selectedDocumentDetail(w http.ResponseWriter, r *http.Request, u user, draftOnly bool) error {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if len(r.Header.Values("Authorization")) != 0 {
		return missingDocumentDetail
	}
	entity, err := pathID(r, "entity")
	if err != nil {
		return missingDocumentDetail
	}
	outlet, err := pathID(r, "outlet")
	if err != nil {
		return missingDocumentDetail
	}
	id, err := pathID(r, "document")
	if err != nil {
		return missingDocumentDetail
	}
	var reference *documentref.Reference
	if draftOnly {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			return badInput
		}
	} else {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return badInput
		}
		var valid bool
		reference, valid = documentref.TakeQuery(query)
		if !valid || reference == nil || len(query) != 0 {
			return badInput
		}
	}
	// Check the branch and selection criterion, then read the authorized
	// attachment metadata and audit in one read-only database snapshot.
	// A concurrent native move/correction cannot mix rows from both versions.
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := scopedMembership(tx, u.id, entity, "read_business_data")
	if err != nil {
		return err
	}
	statement := selectDocuments + " AND d.id=$4"
	args := []any{m.ID, m.id, outlet, id}
	if draftOnly {
		statement += " AND d.status='draft'"
	} else {
		statement += " AND d.document_type=$5 AND d.letter=$6 AND d.point_of_sale=$7 AND d.number=$8"
		args = append(args, reference.DocumentType, reference.Letter, reference.PointOfSale, reference.Number)
	}
	item, err := scanDocument(tx.QueryRowContext(r.Context(), statement, args...), m.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return missingDocumentDetail
	}
	if err != nil {
		return err
	}
	view, err := readVisibleDocumentDetail(tx, item)
	if err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, view)
	return nil
}

var missingDocumentDetail = problem{404, "No se encontró el comprobante."}

var documentAuditLabels = map[string]string{
	"create": "Alta", "update": "Modificación", "delete": "Baja", "review": "Revisión", "attach": "Adjunto",
}

type documentAuditEvent struct {
	Action string `json:"accion"`
	Label  string `json:"accion_nombre"`
	Actor  string `json:"actor"`
	At     string `json:"fecha"`
	Reason string `json:"motivo"`
}

type documentView struct {
	documentListItem
	Attachments []documentAttachment `json:"adjuntos"`
	Audit       []documentAuditEvent `json:"auditoria"`
}

func readDocumentView(tx *sql.Tx, m entityMembership, id int64) (documentView, error) {
	item, err := scanDocument(tx.QueryRow(selectDocuments+" AND d.id=$4", m.ID, m.id, nil, id), m.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return documentView{}, missingDocumentDetail
	}
	if err != nil {
		return documentView{}, err
	}
	return readVisibleDocumentDetail(tx, item)
}

func readVisibleDocumentDetail(tx *sql.Tx, item documentListItem) (documentView, error) {
	attachments, err := readDocumentAttachments(tx, item.ID)
	if err != nil {
		return documentView{}, err
	}
	rows, err := tx.Query(`SELECT a.action,COALESCE(e.handle,u.username),a.occurred_at,a.reason
		FROM document_audit a JOIN users u ON u.id=a.actor_id
		LEFT JOIN organization_employees e ON e.user_id=u.id AND e.entity_id=$2
		WHERE a.document_id=$1 ORDER BY a.occurred_at,a.id`, item.ID, item.Entity)
	if err != nil {
		return documentView{}, err
	}
	defer rows.Close()
	view := documentView{item, attachments, []documentAuditEvent{}}
	for rows.Next() {
		var event documentAuditEvent
		if err := rows.Scan(&event.Action, &event.Actor, &event.At, &event.Reason); err != nil {
			return documentView{}, err
		}
		event.Label = documentAuditLabels[event.Action]
		view.Audit = append(view.Audit, event)
	}
	return view, rows.Err()
}

func documentTotals(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	only, err := documentOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	rows, err := tx.Query("SELECT d.currency,d.document_type,d.total_minor"+documentScope, m.ID, m.id, only)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	type bucket struct {
		count int
		sum   big.Int
	}
	buckets := map[string]*bucket{}
	currencies := []string{}
	count := 0
	for rows.Next() {
		var currency, kind string
		var amount int64
		if err := rows.Scan(&currency, &kind, &amount); err != nil {
			return 0, nil, err
		}
		if buckets[currency] == nil {
			buckets[currency] = &bucket{}
			currencies = append(currencies, currency)
		}
		item := buckets[currency]
		item.count++
		count++
		if kind == "credit_note" {
			amount = -amount
		}
		item.sum.Add(&item.sum, big.NewInt(amount))
	}
	slices.Sort(currencies)
	totals := []map[string]any{}
	for _, currency := range currencies {
		item := buckets[currency]
		totals = append(totals, map[string]any{"moneda": currency, "cantidad": item.count, "total": minorString(&item.sum)})
	}
	return 200, map[string]any{"cantidad": count, "totales": totals}, rows.Err()
}
