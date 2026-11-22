package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var importConflict = problem{409, "La clave de operación ya se usó con otros datos."}

func (a *app) importReconciliationSource(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	id, err := pathID(r, "run")
	if err != nil {
		return err
	}
	if err := a.reconcileTransientScope(r, u, entity, id, false, 0); err != nil {
		return err
	}
	limits := maps.Clone(reconciliationCSVFormLimits)
	limits["side"], limits["expected_revision"], limits["operation_key"] = 32, 128, 256
	data, form, err := readReconciliationCSVUpload(w, r, limits)
	if err != nil {
		return err
	}
	config, err := decodeReconciliationPreview(form)
	if err != nil {
		return err
	}
	if err := a.reconcileCSVConfigScope(r, u, entity, id, false, config); err != nil {
		return err
	}
	side := form["side"]
	switch side {
	case "payments", "cash_flow":
	default:
		return problem{422, "El tipo de archivo no es válido."}
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(form["expected_revision"]), 10, 64)
	if err != nil {
		return problem{422, "La revisión esperada no es válida."}
	}
	key := form["operation_key"]
	if key == "" || utf8.RuneCountInString(key) > 64 {
		return problem{422, "La clave de operación no es válida."}
	}
	parsed, err := parseReconciliationFixedCSV(data, config, 20000)
	if err != nil {
		return err
	}
	if len(parsed) == 0 {
		return problem{422, "El archivo debe contener al menos un registro."}
	}
	mapping, err := json.Marshal(config.mapping)
	if err != nil {
		return err
	}
	formats, err := json.Marshal(map[string]string{
		"delimiter": config.delimiter, "date_format": config.date, "decimal_separator": config.separator,
	})
	if err != nil {
		return err
	}

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockReconciliationRun(tx, u, entity, id, false); err != nil {
		return err
	}
	run, err := readReconciliationCSVScope(tx, u, entity, id, config.outlet)
	if err != nil {
		return err
	}
	if err := validateReconciliationCSVOutlets(tx, id, config); err != nil {
		return err
	}
	replayed, err := readReconciliationSourceReplay(tx, id, key, side, config, string(mapping), string(formats), parsed)
	if err != nil {
		return err
	}
	if replayed != 0 {
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, http.StatusCreated, map[string]any{"id": replayed, "revision": run.Revision, "replayed": true})
		return nil
	}
	if run.State != "draft" {
		return problem{409, "La conciliación ya no admite cambios de archivos."}
	}
	if revision < 1 {
		return problem{422, "La revisión esperada no es válida."}
	}
	change, err := tx.Exec("UPDATE reconciliation_read_runs SET revision=revision+1,updated_at=$1 WHERE id=$2 AND state='draft' AND revision=$3", a.reconciliationRowTime(), id, revision)
	if err != nil {
		return err
	}
	n, err := change.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return problem{409, "La conciliación cambió. Recargá la página."}
	}
	if run.Counts.Sources >= 10 || run.Counts.Rows+int64(len(parsed)) > 20000 {
		return problem{422, "La importación supera los límites de la conciliación."}
	}
	label, err := nextReconciliationSourceLabel(tx, id, side)
	if err != nil {
		return err
	}
	var sourceID, rowID int64
	if err := tx.QueryRow("SELECT source_id+1,row_id FROM reconciliation_source_highwater WHERE singleton=1 FOR UPDATE").Scan(&sourceID, &rowID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO reconciliation_read_sources
		(id,run_id,side,label,mapping,formats,default_currency,default_outlet_id,row_count,operation_key,imported_by_id,imported_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, sourceID, id, side, label, string(mapping), string(formats), config.currency,
		optionalReconciliationOutlet(config.outlet), len(parsed), key, u.id, a.reconciliationRowTime()); err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO reconciliation_detail_rows(run_id,id,source_id,outlet_id,record_number,payload) VALUES($1,$2,$3,$4,$5,$6)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range parsed {
		rowID++
		payload, err := reconciliationSourceRowJSON(rowID, label, side, row)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(id, rowID, sourceID, row.Outlet, row.Record, payload); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := reconciliationWireTime(&now); err != nil {
		return err
	}
	event, err := json.Marshal(map[string]any{
		"action": "import", "actor": u.name, "occurred_at": now, "run_revision": revision + 1, "source_id": sourceID,
		"payment_row_id": nil, "cash_flow_row_id": nil, "match_id": nil,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO reconciliation_detail_events(run_id,ordinal,payload,actor_id)
		VALUES($1,(SELECT COALESCE(MAX(ordinal),0)+1 FROM reconciliation_detail_events WHERE run_id=$2),$3,$4)`,
		id, id, string(event), u.id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusCreated, map[string]any{"id": sourceID, "label": label, "row_count": len(parsed), "revision": revision + 1})
	return nil
}

func optionalReconciliationOutlet(outlet int64) any {
	if outlet == 0 {
		return nil
	}
	return outlet
}

func nextReconciliationSourceLabel(tx *sql.Tx, id int64, side string) (string, error) {
	prefix := "Pagos"
	if side == "cash_flow" {
		prefix = "Caja"
	}
	rows, err := tx.Query("SELECT label FROM reconciliation_read_sources WHERE run_id=$1 AND side=$2", id, side)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	seq := 0
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(label, prefix+" ")); err == nil && n > seq {
			seq = n
		}
	}
	return fmt.Sprintf("%s %d", prefix, seq+1), rows.Err()
}

func reconciliationSourceRowJSON(id int64, label, side string, row reconciliationPreviewRow) (string, error) {
	optional := func(value string) any {
		if value == "" {
			return nil
		}
		return value
	}
	p, err := json.Marshal(map[string]any{
		"id": id, "record_number": row.Record, "outlet_id": row.Outlet, "date": row.Date, "amount": row.Amount,
		"currency": row.Currency, "currency_source": row.CurrencySource, "category": optional(row.Category),
		"description": optional(row.Description), "payment_method": optional(row.Method), "reference": optional(row.Reference),
		"vat_rate": storedCSVVATRate(row.VATRate), "vat_amount": row.VATAmount, "source": label, "side": side,
	})
	return string(p), err
}

func readReconciliationSourceReplay(tx *sql.Tx, run int64, key, side string, cfg reconciliationPreviewConfig, mapping, formats string, parsed []reconciliationPreviewRow) (int64, error) {
	var source, count int64
	var outlet sql.NullInt64
	var storedMap, storedFormats, currency, label, storedSide string
	err := tx.QueryRow(`SELECT id,mapping,formats,default_currency,default_outlet_id,row_count,label,side
		FROM reconciliation_read_sources WHERE run_id=$1 AND operation_key=$2`, run, key).
		Scan(&source, &storedMap, &storedFormats, &currency, &outlet, &count, &label, &storedSide)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if storedMap != mapping || storedFormats != formats || currency != cfg.currency || outlet.Int64 != cfg.outlet || outlet.Valid != (cfg.outlet != 0) ||
		count != int64(len(parsed)) || storedSide != side {
		return 0, importConflict
	}
	rows, err := tx.Query("SELECT id,payload FROM reconciliation_detail_rows WHERE run_id=$1 AND source_id=$2 ORDER BY record_number", run, source)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		if i >= len(parsed) {
			return 0, importConflict
		}
		var rowID int64
		var payload string
		if err := rows.Scan(&rowID, &payload); err != nil {
			return 0, err
		}
		expected, err := reconciliationSourceRowJSON(rowID, label, side, parsed[i])
		if err != nil {
			return 0, err
		}
		if expected != payload {
			return 0, importConflict
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if i != len(parsed) {
		return 0, importConflict
	}
	return source, nil
}
