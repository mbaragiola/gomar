package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"
	"unicode/utf8"
)

const (
	recordPayments  = "record_supplier_payments"
	maxPayableMinor = 99999999999999
	payablesDate    = "2006-01-02"
)

var (
	missingPayable    = problem{404, "No se encontró la cuenta a pagar."}
	missingPayment    = problem{404, "No se encontró el pago."}
	noFinancialOutlet = problem{403, "Necesitás al menos un local autorizado para realizar esta acción."}
	payableStates     = map[string]string{"open": "Pendiente", "partial": "Pago parcial", "settled": "Saldada"}
	movementKinds     = map[string]string{"post": "Alta en cuentas a pagar", "payment": "Pago", "reversal": "Reversión"}
)

var payablesLocation = func() *time.Location {
	loc, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		panic(err) // The Go binary embeds the time zone database.
	}
	return loc
}()

func (a *app) registerPayables(add func(string, string, http.HandlerFunc)) {
	const base = "/api/v1/entities/{entity}"
	add(base+"/documents/{document}/payable", "POST", a.scopedRequest("read_business_data", a.postPayable, true))
	add(base+"/payables", "GET", a.financialRoute(recordPayments, a.listPayables, false))
	add(base+"/payables/{payable}", "GET", a.financialRoute(recordPayments, a.payableDetail, false))
	add(base+"/payments", "GET", a.financialRoute(recordPayments, listPayments, false))
	add(base+"/payments", "POST", a.financialRoute(recordPayments, a.createPaymentDraft, true))
	add(base+"/payments/{payment}", "GET", a.financialRoute(recordPayments, paymentDetail, false))
	add(base+"/payments/{payment}/post", "POST", a.financialRoute(recordPayments, a.postPayment, true))
	add(base+"/payments/{payment}/reverse", "POST", a.financialRoute("reverse_supplier_payments", a.reversePayment, true))
	add(base+"/payments/{payment}/receipt", "GET", a.protected(a.downloadReceipt))
	add(base+"/payments/{payment}/receipt", "POST", a.protected(a.attachReceipt))
}

// When used in a SELECT, these joins restrict the outlet to the current grant; mutations apply their active-outlet checks separately.
const payableOutletJoin = ` JOIN outlets o ON o.id=p.outlet_id AND o.entity_id=p.entity_id
	JOIN outlet_grants g ON g.outlet_id=o.id AND g.membership_id=$1 `

func (a *app) financialRoute(capability string, next scopedHandler, readBody bool) http.HandlerFunc {
	return a.scopedRequest(capability, func(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
		if err := requireFinancialGrant(tx, m, readBody); err != nil {
			return 0, nil, err
		}
		return next(r, tx, m, body)
	}, readBody)
}

func requireFinancialOutlet(tx *sql.Tx, m entityMembership) error {
	return requireFinancialGrant(tx, m, true)
}

func requireFinancialGrant(tx *sql.Tx, m entityMembership, activeOnly bool) error {
	var granted bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
		WHERE g.membership_id=$1 AND o.entity_id=$2 AND (o.active OR NOT $3::boolean))`, m.id, m.ID, activeOnly).Scan(&granted)
	if err == nil && !granted {
		return noFinancialOutlet
	}
	return err
}

func requireFinancialHistoryOutlet(tx *sql.Tx, m entityMembership, outletID int64) error {
	var granted bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
		WHERE g.membership_id=$1 AND o.entity_id=$2 AND o.id=$3)`, m.id, m.ID, outletID).Scan(&granted)
	if err == nil && !granted {
		return missingOutlet
	}
	return err
}

func (a *app) payableNow() time.Time {
	if a.payablesClock != nil {
		return a.payablesClock().UTC()
	}
	return time.Now().UTC()
}

// Keep whole seconds in the historical form. Nonzero fractions use six
// digits, matching Django's microsecond precision without rounding.
func formatPayableStorageTime(at time.Time) string {
	at = at.UTC().Truncate(time.Microsecond)
	if at.Nanosecond() == 0 {
		return at.Format(time.RFC3339)
	}
	return at.Format("2006-01-02T15:04:05.000000Z07:00")
}

func payableMoney(amount int64) string {
	return minorString(big.NewInt(amount))
}

// Accept plain decimal text with at most two fractional digits. A leading
// plus is valid; a leading minus is valid only for zero, so both signed zeros
// reach the same draft and allocation checks as unsigned zero.
func parsePayableMoney(raw, label string) (int64, error) {
	invalid := problem{422, label + ": ingresá un importe válido."}
	normalized := strings.ReplaceAll(strings.TrimSpace(raw), ",", ".")
	negative := strings.HasPrefix(normalized, "-")
	if negative || strings.HasPrefix(normalized, "+") {
		normalized = normalized[1:]
	}
	whole, fraction, hasPoint := strings.Cut(normalized, ".")
	if (whole == "" && (!hasPoint || fraction == "")) || len(fraction) > 2 {
		return 0, invalid
	}
	digits := whole + fraction
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, invalid
		}
	}
	minor, err := strconv.ParseInt(digits+strings.Repeat("0", 2-len(fraction)), 10, 64)
	if err != nil || minor > maxPayableMinor || (negative && minor != 0) {
		return 0, invalid
	}
	return minor, nil
}

func decodePayables(body []byte, target any, allowedFields ...string) error {
	if len(body) == 0 {
		return badInput
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		return badJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return badInput
	}
	// The struct decoder also accepts case-insensitive matches.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return badInput
	}
	for field := range fields {
		if !slices.Contains(allowedFields, field) {
			return badInput
		}
	}
	return nil
}

func validatePayableReason(raw, missing string) (string, error) {
	reason := strings.TrimFunc(raw, func(r rune) bool {
		// Python's str.strip also treats U+001C through U+001F as whitespace.
		return unicode.IsSpace(r) || (r >= '\u001c' && r <= '\u001f')
	})
	if reason == "" {
		return "", problem{422, missing}
	}
	if utf8.RuneCountInString(reason) > 500 || strings.ContainsRune(reason, 0) {
		return "", badInput
	}
	return reason, nil
}

func appendPayablesEvent(tx *sql.Tx, m entityMembership, payable, payment *int64, action, reason, at string, snapshot any) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO ap_financial_events(entity_id,payable_id,payment_id,action,actor_id,reason,snapshot,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, m.ID, payable, payment, action, m.userID, reason, string(data), at)
	return err
}

func appendPayableMovement(tx *sql.Tx, actor, payable int64, payment *int64, kind string, delta, balance int64, reason, at string) error {
	_, err := tx.Exec(`INSERT INTO ap_ledger(payable_id,payment_id,kind,delta_minor,balance_minor,actor_id,reason,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, payable, payment, kind, delta, balance, actor, reason, at)
	return err
}

// payableDocumentEligibility is the document-state portion of the posting
// gate. Native request validation, actor permission and locking remain in the
// write path; a read-only caller cannot guarantee a later write.
func payableDocumentEligibility(posted bool, status, kind string, total int64, supplierMatches bool) (string, error) {
	switch {
	case posted:
		return "already_posted", problem{409, "La factura ya está registrada en cuentas a pagar."}
	case status != "reviewed":
		return "not_reviewed", problem{422, "Solo se puede registrar una factura revisada en cuentas a pagar."}
	case kind != "invoice":
		return "not_invoice", problem{422, "En esta etapa solo se pueden registrar facturas en cuentas a pagar."}
	case total <= 0:
		return "nonpositive_total", problem{422, "La factura debe tener un total mayor que cero."}
	case total > maxPayableMinor:
		return "total_out_of_range", problem{422, "Total: ingresá un importe válido."}
	case !supplierMatches:
		return "supplier_mismatch", problem{422, "El CUIT actual del proveedor no coincide con el comprobante. Corregilo y revisalo de nuevo."}
	default:
		return "", nil
	}
}

func (a *app) postPayable(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	var input struct {
		Reason *string `json:"motivo"`
	}
	if err := decodePayables(body, &input, "motivo"); err != nil {
		return 0, nil, err
	}
	if input.Reason == nil {
		return 0, nil, badInput
	}
	document, err := pathID(r, "document")
	if err != nil {
		return 0, nil, err
	}
	// All accounting writers lock the entity, not just this document's
	// outlet: one payment can allocate to several granted outlets.
	if err := lockPostgresEntity(r.Context(), tx, m.ID); err != nil {
		return 0, nil, err
	}
	var outletID, supplierID, point, number, total int64
	var kind, status, letter, issue, currency, issuer, supplierCUIT, supplierName string
	var due *string
	var posted bool
	err = tx.QueryRow(`SELECT d.outlet_id,d.supplier_id,d.point_of_sale,d.number,d.total_minor,d.document_type,
		d.status,d.letter,d.issue_date,d.due_date,d.currency,d.issuer_cuit,s.cuit,s.name,
		EXISTS(SELECT 1 FROM ap_payables p WHERE p.source_document_id=d.id)`+documentScope+` AND d.id=$4`,
		m.ID, m.id, nil, document).Scan(&outletID, &supplierID, &point, &number, &total, &kind,
		&status, &letter, &issue, &due, &currency, &issuer, &supplierCUIT, &supplierName, &posted)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, problem{404, "No se encontró el comprobante."}
	}
	if err != nil {
		return 0, nil, err
	}
	if !slices.Contains(capabilities[m.role], recordPayments) {
		return 0, nil, forbiddenRole
	}
	reason, err := validatePayableReason(*input.Reason, "El motivo del alta en cuentas a pagar es obligatorio.")
	if err != nil {
		return 0, nil, err
	}
	if _, err := payableDocumentEligibility(posted, status, kind, total, issuer == supplierCUIT); err != nil {
		return 0, nil, err
	}

	reference := documentTypes[kind]
	if letter != "" {
		reference += " " + letter
	}
	reference = fmt.Sprintf("%s %05d-%08d", reference, point, number)
	now := a.payableNow()
	at := formatPayableStorageTime(now)
	// The document timestamp keeps its existing second-precision wire form.
	documentUpdatedAt := now.Format(time.RFC3339)
	// The entity lock also serializes supplier and document writes in PostgreSQL.
	if _, err := tx.Exec("UPDATE documents SET revision=revision+1, updated_at=$1 WHERE id=$2", documentUpdatedAt, document); err != nil {
		return 0, nil, err
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO ap_payables(entity_id,outlet_id,supplier_id,source_document_id,currency,
		original_minor,balance_minor,supplier_name_snapshot,supplier_cuit_snapshot,document_reference_snapshot,
		issue_date_snapshot,due_date_snapshot,posted_by_id,posted_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`,
		m.ID, outletID, supplierID, document, currency, total, total, supplierName, issuer, reference, issue, due, m.userID, at).Scan(&id)
	if err != nil {
		return 0, nil, err
	}
	if err := appendPayableMovement(tx, m.userID, id, nil, "post", total, total, reason, at); err != nil {
		return 0, nil, err
	}
	if err := appendPayablesEvent(tx, m, &id, nil, "post_payable", reason, at, map[string]any{
		"document_id": document, "document_reference": reference, "supplier_id": supplierID,
		"supplier_cuit": issuer, "currency": currency, "amount_minor": total,
	}); err != nil {
		return 0, nil, err
	}
	result, err := loadPayableDetail(tx, m, id, now)
	return 201, result, err
}
