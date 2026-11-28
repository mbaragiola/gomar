package main

import (
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"unicode/utf8"
)

func (a *app) createPaymentDraft(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	var input struct {
		Outlet    *int64  `json:"outlet_id"`
		Supplier  *int64  `json:"supplier_id"`
		Currency  *string `json:"moneda"`
		Total     *string `json:"total"`
		Method    *string `json:"metodo"`
		Reference *string `json:"referencia"`
	}
	input.Reference = new(string) // Omission keeps the empty default; JSON null clears it.
	if err := decodePayables(body, &input, "outlet_id", "supplier_id", "moneda", "total", "metodo", "referencia"); err != nil {
		return 0, nil, err
	}
	if input.Outlet == nil || input.Supplier == nil || input.Currency == nil || input.Total == nil || input.Method == nil || input.Reference == nil {
		return 0, nil, badInput
	}

	if err := lockPostgresEntity(r.Context(), tx, m.ID); err != nil {
		return 0, nil, err
	}
	var allowed bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id
		WHERE o.id=$1 AND o.entity_id=$2 AND o.active=TRUE AND g.membership_id=$3)`,
		*input.Outlet, m.ID, m.id).Scan(&allowed)
	if err != nil {
		return 0, nil, err
	}
	if !allowed {
		return 0, nil, missingOutlet
	}
	err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM suppliers WHERE id=$1 AND entity_id=$2 AND active=TRUE)",
		*input.Supplier, m.ID).Scan(&allowed)
	if err != nil {
		return 0, nil, err
	}
	if !allowed {
		return 0, nil, problem{404, "No se encontró el proveedor."}
	}
	if *input.Currency != "ARS" && *input.Currency != "USD" {
		return 0, nil, problem{422, "La moneda no es válida."}
	}
	if _, known := paymentMethods[*input.Method]; !known {
		return 0, nil, problem{422, "El medio de pago no es válido."}
	}
	total, err := parsePayableMoney(*input.Total, "Total")
	if err != nil {
		return 0, nil, err
	}
	if total == 0 {
		return 0, nil, problem{422, "El total debe ser mayor que cero."}
	}
	reference := strings.TrimSpace(*input.Reference)
	if utf8.RuneCountInString(reference) > 200 || strings.ContainsRune(reference, 0) {
		return 0, nil, badInput
	}

	at := formatPayableStorageTime(a.payableNow())
	var id int64
	err = tx.QueryRow(`INSERT INTO ap_payments(entity_id,outlet_id,supplier_id,currency,total_minor,
		method,reference,status,kind,reversal_reason,created_by_id,created_at,post_key)
		VALUES($1,$2,$3,$4,$5,$6,$7,'draft','payment','',$8,$9,$10) RETURNING id`,
		m.ID, *input.Outlet, *input.Supplier, *input.Currency, total, *input.Method, reference, m.userID, at, randomToken()).Scan(&id)
	if err != nil {
		return 0, nil, err
	}
	if err := appendPayablesEvent(tx, m, nil, &id, "create_draft", "Creación del borrador", at, map[string]any{
		"supplier_id": *input.Supplier, "currency": *input.Currency, "total_minor": total, "method": *input.Method,
	}); err != nil {
		return 0, nil, err
	}
	p, err := visiblePayment(tx, m, id)
	if err != nil {
		return 0, nil, err
	}
	return paymentResponse(tx, p, 201)
}

type payableAllocation struct {
	PayableID   int64 `json:"payable_id"`
	AmountMinor int64 `json:"amount_minor"`
	id          int64
	reference   string
}

func paymentAllocations(tx *sql.Tx, id int64) ([]payableAllocation, error) {
	rows, err := tx.Query(`SELECT a.id,a.payable_id,a.amount_minor,p.document_reference_snapshot
		FROM ap_allocations a JOIN ap_payables p ON p.id=a.payable_id WHERE a.payment_id=$1 ORDER BY a.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var allocations []payableAllocation
	for rows.Next() {
		var a payableAllocation
		if err := rows.Scan(&a.id, &a.PayableID, &a.AmountMinor, &a.reference); err != nil {
			return nil, err
		}
		allocations = append(allocations, a)
	}
	return allocations, rows.Err()
}

type allocationPayable struct {
	supplier          int64
	currency          string
	original, balance int64
}

func loadAllocationPayable(tx *sql.Tx, m entityMembership, id int64) (allocationPayable, error) {
	var p allocationPayable
	err := tx.QueryRow(`SELECT p.supplier_id,p.currency,p.original_minor,p.balance_minor
		FROM ap_payables p`+payableOutletJoin+` WHERE p.entity_id=$2 AND p.id=$3 AND o.active`, m.id, m.ID, id).
		Scan(&p.supplier, &p.currency, &p.original, &p.balance)
	if errors.Is(err, sql.ErrNoRows) {
		err = problem{404, "No se encontró una factura seleccionada."}
	}
	return p, err
}

func applyPaymentAllocation(tx *sql.Tx, m entityMembership, paymentID int64, a payableAllocation,
	balance, delta int64, reverses *int64, kind, reason, at string) error {
	if _, err := tx.Exec("UPDATE ap_payables SET balance_minor=$1 WHERE id=$2", balance, a.PayableID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO ap_allocations(payment_id,payable_id,amount_minor,reverses_id,created_at)
		VALUES($1,$2,$3,$4,$5)`, paymentID, a.PayableID, a.AmountMinor, reverses, at); err != nil {
		return err
	}
	return appendPayableMovement(tx, m.userID, a.PayableID, &paymentID, kind, delta, balance, reason, at)
}

type paymentAllocationInput struct {
	PayableID *int64  `json:"payable_id"`
	Amount    *string `json:"importe"`
}

func (a *paymentAllocationInput) UnmarshalJSON(body []byte) error {
	type allocationFields paymentAllocationInput
	// A new allocation object replaces any fields already in the destination.
	var fields allocationFields
	if err := decodePayables(body, &fields, "payable_id", "importe"); err != nil {
		return err
	}
	*a = paymentAllocationInput(fields)
	return nil
}

func (a *app) postPayment(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	var input struct {
		Key         *string                  `json:"idempotency_key"`
		Allocations []paymentAllocationInput `json:"aplicaciones"`
	}
	if err := decodePayables(body, &input, "idempotency_key", "aplicaciones"); err != nil {
		return 0, nil, err
	}
	if input.Key == nil || input.Allocations == nil {
		return 0, nil, badInput
	}
	for _, requested := range input.Allocations {
		if requested.PayableID == nil || requested.Amount == nil {
			return 0, nil, badInput
		}
	}
	if err := lockPostgresEntity(r.Context(), tx, m.ID); err != nil {
		return 0, nil, err
	}
	p, err := activePaymentFromPath(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	if len(input.Allocations) == 0 {
		return 0, nil, problem{404, "No se encontró una factura seleccionada."}
	}

	allocations := make([]payableAllocation, 0, len(input.Allocations))
	invoices := make(map[int64]allocationPayable, len(input.Allocations))
	for _, requested := range input.Allocations {
		amount, err := parsePayableMoney(*requested.Amount, "Importe")
		if err != nil {
			return 0, nil, err
		}
		allocations = append(allocations, payableAllocation{PayableID: *requested.PayableID, AmountMinor: amount})
	}
	for _, allocation := range allocations {
		invoice, err := loadAllocationPayable(tx, m, allocation.PayableID)
		if err != nil {
			return 0, nil, err
		}
		invoices[allocation.PayableID] = invoice
	}

	key := strings.TrimSpace(*input.Key)
	if key == "" || len(key) > 64 {
		return 0, nil, problem{422, "La clave de idempotencia no es válida."}
	}
	if key != p.Key {
		return 0, nil, problem{422, "La clave de idempotencia no corresponde al borrador."}
	}
	requested := make(map[int64]int64, len(allocations))
	var sum int64
	for _, allocation := range allocations {
		if allocation.AmountMinor == 0 {
			return 0, nil, problem{422, "Cada importe aplicado debe ser mayor que cero."}
		}
		if _, duplicate := requested[allocation.PayableID]; duplicate {
			return 0, nil, problem{422, "Cada factura puede aparecer una sola vez en el pago."}
		}
		requested[allocation.PayableID] = allocation.AmountMinor
		sum += allocation.AmountMinor // Bounded by the 64 KiB JSON body and maxPayableMinor.
	}
	if sum != p.totalMinor {
		return 0, nil, problem{422, "La suma aplicada debe coincidir exactamente con el total del pago."}
	}
	if p.Status != "draft" {
		if p.Kind == "payment" && p.postedBy != nil && *p.postedBy == m.userID {
			saved, err := paymentAllocations(tx, p.ID)
			if err != nil {
				return 0, nil, err
			}
			committed := make(map[int64]int64, len(saved))
			for _, allocation := range saved {
				committed[allocation.PayableID] = allocation.AmountMinor
			}
			if maps.Equal(requested, committed) {
				return paymentResponse(tx, p, 200)
			}
		}
		return 0, nil, problem{409, "El pago ya fue registrado con otro actor o distribución."}
	}

	at := formatPayableStorageTime(a.payableNow())
	if _, err := tx.Exec("UPDATE ap_payments SET status='posted',posted_by_id=$1,posted_at=$2 WHERE id=$3",
		m.userID, at, p.ID); err != nil {
		return 0, nil, err
	}
	reason := fmt.Sprintf("Aplicación del pago %d", p.ID)
	for _, allocation := range allocations {
		invoice := invoices[allocation.PayableID]
		if invoice.supplier != p.Supplier.ID || invoice.currency != p.Currency {
			return 0, nil, problem{422, "Todas las facturas deben ser de la misma entidad, proveedor y moneda."}
		}
		if allocation.AmountMinor > invoice.balance {
			return 0, nil, problem{422, "El importe aplicado supera el saldo de una factura."}
		}
		if err := applyPaymentAllocation(tx, m, p.ID, allocation, invoice.balance-allocation.AmountMinor,
			-allocation.AmountMinor, nil, "payment", reason, at); err != nil {
			return 0, nil, err
		}
	}
	if err := appendPayablesEvent(tx, m, nil, &p.ID, "post_payment", "Registro manual del pago", at, map[string]any{
		"total_minor": p.totalMinor, "currency": p.Currency, "method": p.Method, "allocations": allocations,
	}); err != nil {
		return 0, nil, err
	}
	p, err = visiblePayment(tx, m, p.ID)
	if err != nil {
		return 0, nil, err
	}
	return paymentResponse(tx, p, 200)
}

func (a *app) reversePayment(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	var input struct {
		Reason *string `json:"motivo"`
	}
	if err := decodePayables(body, &input, "motivo"); err != nil {
		return 0, nil, err
	}
	if input.Reason == nil {
		return 0, nil, badInput
	}
	if err := lockPostgresEntity(r.Context(), tx, m.ID); err != nil {
		return 0, nil, err
	}
	p, err := activePaymentFromPath(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	reason, err := validatePayableReason(*input.Reason, "El motivo de la reversión es obligatorio.")
	if err != nil {
		return 0, nil, err
	}
	if p.Status == "reversed" {
		return 0, nil, problem{409, "El pago ya fue revertido."}
	}
	if p.Status != "posted" || p.Kind != "payment" {
		return 0, nil, problem{409, "Solo se puede revertir un pago registrado."}
	}
	allocations, err := paymentAllocations(tx, p.ID)
	if err != nil {
		return 0, nil, err
	}

	at := formatPayableStorageTime(a.payableNow())
	if _, err := tx.Exec("UPDATE ap_payments SET status='reversed' WHERE id=$1", p.ID); err != nil {
		return 0, nil, err
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO ap_payments(entity_id,outlet_id,supplier_id,currency,total_minor,method,reference,
		status,kind,reversal_of_id,reversal_reason,created_by_id,created_at,posted_by_id,posted_at,post_key)
		VALUES($1,$2,$3,$4,$5,$6,$7,'posted','reversal',$8,$9,$10,$11,$12,$13,$14) RETURNING id`, m.ID, p.Outlet.ID, p.Supplier.ID, p.Currency,
		p.totalMinor, p.Method, p.Reference, p.ID, reason, m.userID, at, m.userID, at, randomToken()).Scan(&id)
	if err != nil {
		return 0, nil, err
	}
	for _, allocation := range allocations {
		invoice, err := loadAllocationPayable(tx, m, allocation.PayableID)
		if err != nil {
			return 0, nil, err
		}
		restored := invoice.balance + allocation.AmountMinor
		if restored > invoice.original {
			return 0, nil, problem{409, "El saldo de una factura no permite revertir el pago."}
		}
		if err := applyPaymentAllocation(tx, m, id, allocation, restored, allocation.AmountMinor,
			&allocation.id, "reversal", reason, at); err != nil {
			return 0, nil, err
		}
	}
	if err := appendPayablesEvent(tx, m, nil, &id, "reverse_payment", reason, at, map[string]any{
		"reversal_of": p.ID, "total_minor": p.totalMinor,
	}); err != nil {
		return 0, nil, err
	}
	reversal, err := visiblePayment(tx, m, id)
	if err != nil {
		return 0, nil, err
	}
	return paymentResponse(tx, reversal, 201)
}
