package main

import (
	"cmp"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type payableView struct {
	ID         int64    `json:"id"`
	Document   int64    `json:"documento_id"`
	Reference  string   `json:"referencia"`
	Outlet     outlet   `json:"outlet"`
	Supplier   supplier `json:"supplier"`
	Issue      string   `json:"fecha_emision"`
	Due        *string  `json:"fecha_vencimiento"`
	Currency   string   `json:"moneda"`
	Original   string   `json:"importe_original"`
	Balance    string   `json:"saldo"`
	State      string   `json:"estado"`
	StateLabel string   `json:"estado_nombre"`
	Overdue    bool     `json:"vencida"`
	Posted     string   `json:"registrada"`
}

type payableMovementView struct {
	Kind      string `json:"tipo"`
	KindLabel string `json:"tipo_nombre"`
	Delta     string `json:"variacion"`
	Balance   string `json:"saldo"`
	At        string `json:"fecha"`
	Actor     string `json:"actor"`
	Reason    string `json:"motivo"`
	Payment   *int64 `json:"pago_id"`
}

type payableDetailView struct {
	payableView
	Movements []payableMovementView `json:"movimientos"`
}

type payableScanner interface {
	Scan(...any) error
}

var payableNativeUTCTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)
var payableOrderingTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

type payableChronology struct {
	at     time.Time
	id     int64
	parsed bool
}

func newPayableChronology(raw string, id int64) payableChronology {
	key := payableChronology{id: id}
	if payableOrderingTime.MatchString(raw) {
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err == nil && at.Year() >= 1 {
			key.at, key.parsed = at, true
		}
	}
	return key
}

func sortPayableChronology[T any](rows []T, key func(T) payableChronology, descending bool) {
	// SQL has already applied the legacy textual order. Hold any unrecognized
	// timestamp in its original slot and sort only the recognized instants.
	known := make([]T, 0, len(rows))
	for _, row := range rows {
		if key(row).parsed {
			known = append(known, row)
		}
	}
	slices.SortFunc(known, func(a, b T) int {
		left, right := key(a), key(b)
		order := left.at.Compare(right.at)
		if order == 0 {
			order = cmp.Compare(left.id, right.id)
		}
		if descending {
			return -order
		}
		return order
	})
	next := 0
	for i, row := range rows {
		if key(row).parsed {
			rows[i] = known[next]
			next++
		}
	}
}

func payableAPITime(stored string) string {
	// Go writes UTC with Z; the historical projection already uses +00:00.
	// Preserve fractional precision and do not relabel unrecognized storage.
	if payableNativeUTCTime.MatchString(stored) {
		if _, err := time.Parse(time.RFC3339Nano, stored); err == nil {
			return strings.TrimSuffix(stored, "Z") + "+00:00"
		}
	}
	return stored
}

func scanPayable(row payableScanner, today string) (payableView, error) {
	var p payableView
	var original, balance int64
	if err := row.Scan(&p.ID, &p.Document, &p.Reference, &p.Outlet.ID, &p.Outlet.Name,
		&p.Supplier.ID, &p.Supplier.Name, &p.Supplier.CUIT, &p.Issue, &p.Due, &p.Currency,
		&original, &balance, &p.State, &p.Posted); err != nil {
		return p, err
	}
	p.Original, p.Balance = payableMoney(original), payableMoney(balance)
	p.StateLabel = payableStates[p.State]
	p.Overdue = balance > 0 && p.Due != nil && *p.Due < today
	p.Posted = payableAPITime(p.Posted)
	return p, nil
}

const selectPayables = `SELECT p.id,p.source_document_id,p.document_reference_snapshot,p.outlet_id,o.name,
	p.supplier_id,p.supplier_name_snapshot,p.supplier_cuit_snapshot,p.issue_date_snapshot,p.due_date_snapshot,
	p.currency,p.original_minor,p.balance_minor,p.state,p.posted_at
	FROM ap_payables p` + payableOutletJoin + ` WHERE p.entity_id=$2 `

func (a *app) listPayables(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, nil, badInput
	}
	outletID, err := documentOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	lastValue := func(key string) string {
		values := query[key]
		if len(values) == 0 {
			return ""
		}
		return values[len(values)-1]
	}
	var supplierID *int64
	if query.Has("supplier_id") {
		id, err := strconv.ParseInt(lastValue("supplier_id"), 10, 64)
		if err != nil {
			return 0, nil, badInput
		}
		supplierID = &id
	}
	from, to := lastValue("due_from"), lastValue("due_to")
	for _, key := range []string{"due_from", "due_to"} {
		if query.Has(key) {
			if _, err := time.Parse(payablesDate, lastValue(key)); err != nil {
				return 0, nil, badInput
			}
		}
	}
	currency, state := lastValue("currency"), lastValue("state")
	if query.Has("currency") && currency != "ARS" && currency != "USD" {
		return 0, nil, problem{422, "La moneda no es válida."}
	}
	if _, known := payableStates[state]; query.Has("state") && !known {
		return 0, nil, problem{422, "El estado no es válido."}
	}
	rows, err := tx.Query(selectPayables+` AND (CAST($3 AS BIGINT) IS NULL OR p.outlet_id=$4) AND (CAST($5 AS BIGINT) IS NULL OR p.supplier_id=$6)
		AND ($7='' OR p.due_date_snapshot>=$8) AND ($9='' OR p.due_date_snapshot<=$10)
		AND ($11='' OR p.currency=$12) AND ($13='' OR p.state=$14) ORDER BY p.due_date_snapshot NULLS FIRST,p.id`,
		m.id, m.ID, outletID, outletID, supplierID, supplierID, from, from, to, to, currency, currency, state, state)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	items := []payableView{}
	today := a.payableNow().In(payablesLocation).Format(payablesDate)
	for rows.Next() {
		p, err := scanPayable(rows, today)
		if err != nil {
			return 0, nil, err
		}
		items = append(items, p)
	}
	return 200, items, rows.Err()
}

func (a *app) payableDetail(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	id, err := pathID(r, "payable")
	if err != nil {
		return 0, nil, err
	}
	p, err := loadPayableDetail(tx, m, id, a.payableNow())
	return 200, p, err
}

func loadPayableDetail(tx *sql.Tx, m entityMembership, id int64, now time.Time) (payableDetailView, error) {
	row := tx.QueryRow(selectPayables+" AND p.id=$3", m.id, m.ID, id)
	p, err := scanPayable(row, now.In(payablesLocation).Format(payablesDate))
	detail := payableDetailView{payableView: p, Movements: []payableMovementView{}}
	if errors.Is(err, sql.ErrNoRows) {
		return detail, missingPayable
	}
	if err != nil {
		return detail, err
	}
	rows, err := tx.Query(`SELECT l.id,l.kind,l.delta_minor,l.balance_minor,l.occurred_at,COALESCE(e.handle,u.username),l.reason,l.payment_id
		FROM ap_ledger l JOIN users u ON u.id=l.actor_id
		LEFT JOIN organization_employees e ON e.user_id=u.id AND e.entity_id=$2
		WHERE l.payable_id=$1 ORDER BY l.occurred_at,l.id`, id, m.ID)
	if err != nil {
		return detail, err
	}
	defer rows.Close()
	type timedMovement struct {
		order payableChronology
		view  payableMovementView
	}
	var movements []timedMovement
	for rows.Next() {
		var movement payableMovementView
		var movementID, delta, balance int64
		if err := rows.Scan(&movementID, &movement.Kind, &delta, &balance, &movement.At, &movement.Actor, &movement.Reason,
			&movement.Payment); err != nil {
			return detail, err
		}
		movement.KindLabel = movementKinds[movement.Kind]
		movement.Delta, movement.Balance = payableMoney(delta), payableMoney(balance)
		order := newPayableChronology(movement.At, movementID)
		movement.At = payableAPITime(movement.At)
		movements = append(movements, timedMovement{order: order, view: movement})
	}
	if err := rows.Err(); err != nil {
		return detail, err
	}
	sortPayableChronology(movements, func(row timedMovement) payableChronology { return row.order }, false)
	for _, row := range movements {
		detail.Movements = append(detail.Movements, row.view)
	}
	return detail, nil
}

type paymentSupplier struct {
	ID   int64  `json:"id"`
	Name string `json:"razon_social"`
}

type paymentView struct {
	ID          int64           `json:"id"`
	Outlet      outlet          `json:"outlet"`
	Supplier    paymentSupplier `json:"supplier"`
	Currency    string          `json:"moneda"`
	Total       string          `json:"total"`
	Method      string          `json:"metodo"`
	MethodLabel string          `json:"metodo_nombre"`
	Reference   string          `json:"referencia"`
	Status      string          `json:"estado"`
	StatusLabel string          `json:"estado_nombre"`
	Kind        string          `json:"tipo"`
	ReversalOf  *int64          `json:"reversion_de"`
	Reason      string          `json:"motivo_reversion"`
	CreatedAt   string          `json:"creado"`
	PostedAt    *string         `json:"registrado"`
	HasReceipt  bool            `json:"tiene_comprobante"`
	Key         string          `json:"idempotency_key"`
}

type paymentRecord struct {
	paymentView
	totalMinor int64
	postedBy   *int64
	order      payableChronology
}

type paymentAllocationView struct {
	PayableID int64  `json:"payable_id"`
	Reference string `json:"referencia"`
	Amount    string `json:"importe"`
}

type paymentDetailView struct {
	paymentView
	Allocations []paymentAllocationView `json:"aplicaciones"`
}

var paymentMethods = map[string]string{"cash": "Efectivo", "bank_transfer": "Transferencia bancaria"}
var paymentStatuses = map[string]string{"draft": "Borrador", "posted": "Registrado", "reversed": "Revertido"}

const selectVisiblePayments = `SELECT p.id,p.outlet_id,o.name,p.supplier_id,s.name,p.currency,p.total_minor,
	p.method,p.reference,p.status,p.kind,p.reversal_of_id,p.reversal_reason,p.created_at,p.posted_at,p.post_key,p.posted_by_id,
	EXISTS(SELECT 1 FROM ap_receipts receipt WHERE receipt.payment_id=p.id)
	FROM ap_payments p` + payableOutletJoin + ` JOIN suppliers s ON s.id=p.supplier_id AND s.entity_id=p.entity_id
	WHERE p.entity_id=$2 AND NOT EXISTS (
		SELECT 1 FROM ap_allocations a JOIN ap_payables invoice ON invoice.id=a.payable_id
		WHERE a.payment_id=p.id AND (invoice.entity_id<>p.entity_id OR NOT EXISTS (
			SELECT 1 FROM outlets source JOIN outlet_grants scope_grant ON scope_grant.outlet_id=source.id
			WHERE source.id=invoice.outlet_id AND source.entity_id=p.entity_id AND source.active=TRUE
			AND scope_grant.membership_id=g.membership_id))
	) `

func scanPayment(row payableScanner) (paymentRecord, error) {
	var p paymentRecord
	if err := row.Scan(&p.ID, &p.Outlet.ID, &p.Outlet.Name, &p.Supplier.ID, &p.Supplier.Name, &p.Currency,
		&p.totalMinor, &p.Method, &p.Reference, &p.Status, &p.Kind, &p.ReversalOf, &p.Reason, &p.CreatedAt,
		&p.PostedAt, &p.Key, &p.postedBy, &p.HasReceipt); err != nil {
		return p, err
	}
	p.Total = payableMoney(p.totalMinor)
	p.MethodLabel, p.StatusLabel = paymentMethods[p.Method], paymentStatuses[p.Status]
	p.order = newPayableChronology(p.CreatedAt, p.ID)
	p.CreatedAt = payableAPITime(p.CreatedAt)
	if p.PostedAt != nil {
		*p.PostedAt = payableAPITime(*p.PostedAt)
	}
	return p, nil
}

func listPayments(_ *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	rows, err := tx.Query(selectVisiblePayments+" ORDER BY p.created_at DESC,p.id DESC", m.id, m.ID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var payments []paymentRecord
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return 0, nil, err
		}
		payments = append(payments, p)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	sortPayableChronology(payments, func(row paymentRecord) payableChronology { return row.order }, true)
	items := make([]paymentView, 0, len(payments))
	for _, p := range payments {
		items = append(items, p.paymentView)
	}
	return 200, items, nil
}

func visiblePayment(tx *sql.Tx, m entityMembership, id int64) (paymentRecord, error) {
	p, err := scanPayment(tx.QueryRow(selectVisiblePayments+" AND p.id=$3", m.id, m.ID, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = missingPayment
	}
	return p, err
}

func paymentFromPath(r *http.Request, tx *sql.Tx, m entityMembership) (paymentRecord, error) {
	id, err := pathID(r, "payment")
	if err != nil {
		return paymentRecord{}, err
	}
	return visiblePayment(tx, m, id)
}

func paymentDetail(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	p, err := paymentFromPath(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	return paymentResponse(tx, p, 200)
}

func paymentResponse(tx *sql.Tx, p paymentRecord, status int) (int, any, error) {
	allocations, err := paymentAllocations(tx, p.ID)
	if err != nil {
		return 0, nil, err
	}
	result := paymentDetailView{paymentView: p.paymentView, Allocations: make([]paymentAllocationView, 0, len(allocations))}
	for _, a := range allocations {
		result.Allocations = append(result.Allocations, paymentAllocationView{a.PayableID, a.reference, payableMoney(a.AmountMinor)})
	}
	return status, result, nil
}
