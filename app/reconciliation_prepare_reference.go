package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var unsupportedReconciliationPrepare = problem{503, "Solo está disponible preparar las fuentes CSV compatibles."}

func (a *app) prepareReconciliation(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	id, err := pathID(r, "run")
	if err != nil {
		return err
	}
	var input struct {
		Revision *int64  `json:"expected_revision"`
		Key      *string `json:"operation_key"`
	}
	if err := parseJSON(w, r, &input); err != nil {
		return err
	}
	if input.Revision == nil || input.Key == nil {
		return badInput
	}
	if *input.Key == "" || utf8.RuneCountInString(*input.Key) > 64 {
		return problem{422, "La clave de operación no es válida."}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockReconciliationRun(tx, u, entity, id, false); err != nil {
		return err
	}
	run, err := readReconciliationCSVScope(tx, u, entity, id, 0)
	if err != nil {
		return err
	}
	run.Outlets, err = reconciliationOutlets(tx, id)
	if err != nil {
		return err
	}
	var prior string
	err = tx.QueryRow("SELECT (CAST(payload AS json)->>'action') FROM reconciliation_detail_events WHERE run_id=$1 AND operation_key=$2", id, *input.Key).Scan(&prior)
	if err == nil {
		if prior != "prepare" {
			return problem{409, "La clave de operación ya se usó para otra acción."}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, http.StatusOK, run)
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if run.State != "draft" {
		return problem{409, "La conciliación ya fue preparada."}
	}
	if run.Rules != 1 {
		return unsupportedReconciliationPrepare
	}
	if err := validatePrepareSources(tx, run); err != nil {
		return err
	}
	if *input.Revision < 1 {
		return problem{422, "La revisión esperada no es válida."}
	}
	if *input.Revision != run.Revision {
		return problem{409, "La conciliación cambió. Recargá la página."}
	}
	rows, categories, err := readPrepareRows(tx, run)
	if err != nil {
		return err
	}
	group := buildNonReferenceGroups
	if run.MatchReference {
		group = buildExactReferenceGroups
	}
	groups, err := group(rows)
	if err != nil {
		return unsupportedReconciliationPrepare
	}
	if err := writePrepareGroups(tx, id, groups, categories); err != nil {
		return err
	}
	change, err := tx.Exec("UPDATE reconciliation_read_runs SET state='review',revision=revision+1,updated_at=$1 WHERE id=$2 AND state='draft' AND revision=$3", a.reconciliationRowTime(), id, *input.Revision)
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
	run.State = "review"
	run.Revision++
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := reconciliationWireTime(&now); err != nil {
		return err
	}
	event, err := json.Marshal(map[string]any{
		"action": "prepare", "actor": u.name, "occurred_at": now, "run_revision": run.Revision,
		"source_id": nil, "payment_row_id": nil, "cash_flow_row_id": nil, "match_id": nil,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO reconciliation_detail_events(run_id,ordinal,payload,actor_id,operation_key)
		VALUES($1,(SELECT COALESCE(MAX(ordinal),0)+1 FROM reconciliation_detail_events WHERE run_id=$2),$3,$4,$5)`,
		id, id, string(event), u.id, *input.Key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, run)
	return nil
}

func validatePrepareSources(tx *sql.Tx, run reconciliationReadRun) error {
	var pays, cash, unmapped, unsupported, existingGroups int
	err := tx.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE side='payments' AND row_count>0),
		COUNT(*) FILTER (WHERE side='cash_flow' AND row_count>0),
		COUNT(*) FILTER (WHERE json_typeof(CAST(mapping AS json)->'reference') IS NULL),
		COUNT(*) FILTER (WHERE operation_key IS NULL OR imported_by_id IS NULL OR row_count<=0),
		(SELECT COUNT(*) FROM reconciliation_detail_groups WHERE run_id=$1)
		FROM reconciliation_read_sources WHERE run_id=$2`, run.ID, run.ID).Scan(&pays, &cash, &unmapped, &unsupported, &existingGroups)
	if err != nil {
		return err
	}
	if pays == 0 || cash == 0 {
		return problem{422, "Importá archivos no vacíos de pagos y de movimientos de caja."}
	}
	if run.MatchReference && unmapped != 0 {
		return problem{422, "Todos los archivos deben mapear la referencia para usarla como regla."}
	}
	if unsupported != 0 || existingGroups != 0 || run.Counts.Rows > 20000 || run.Counts.Confirmed != 0 || run.Counts.Rejected != 0 {
		return unsupportedReconciliationPrepare
	}
	return nil
}

type reconciliationComparables struct {
	Category, VATRate, VATAmount string
}

func readPrepareRows(tx *sql.Tx, run reconciliationReadRun) ([]reconciliationReferenceRow, map[int64]reconciliationComparables, error) {
	query, err := tx.Query(`SELECT r.id,r.outlet_id,s.side,r.payload,s.default_outlet_id,COALESCE(s.default_currency,''),json_typeof(CAST(s.mapping AS json)->'currency') IS NOT NULL,json_typeof(CAST(s.mapping AS json)->'outlet') IS NOT NULL
		FROM reconciliation_detail_rows r JOIN reconciliation_read_sources s ON s.id=r.source_id AND s.run_id=r.run_id
		WHERE r.run_id=$1 ORDER BY r.source_id,r.record_number`, run.ID)
	if err != nil {
		return nil, nil, err
	}
	defer query.Close()
	data := []reconciliationReferenceRow{}
	categories := map[int64]reconciliationComparables{}
	for query.Next() {
		var item reconciliationReferenceRow
		var raw, currency string
		var outlet sql.NullInt64
		var mappedCurrency, mappedOutlet bool
		err := query.Scan(&item.ID, &item.Outlet, &item.Side, &raw, &outlet, &currency, &mappedCurrency, &mappedOutlet)
		if err != nil {
			return nil, nil, err
		}
		var payload struct {
			Date           string  `json:"date"`
			Amount         string  `json:"amount"`
			Currency       string  `json:"currency"`
			CurrencySource string  `json:"currency_source"`
			Category       *string `json:"category"`
			Reference      *string `json:"reference"`
			VATRate        *string `json:"vat_rate"`
			VATAmount      *string `json:"vat_amount"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, nil, unsupportedReconciliationPrepare
		}
		validCurrency := payload.CurrencySource == "supplied" && payload.Currency == currency
		if mappedCurrency {
			validCurrency = currency == "" && payload.CurrencySource == "imported"
		}
		if !validCurrency ||
			(!mappedOutlet && (!outlet.Valid || item.Outlet != outlet.Int64)) || !strings.Contains(payload.Amount, ".") {
			return nil, nil, unsupportedReconciliationPrepare
		}
		item.Amount, err = strconv.ParseInt(strings.Replace(payload.Amount, ".", "", 1), 10, 64)
		if err != nil || len(data) >= 20000 {
			return nil, nil, unsupportedReconciliationPrepare
		}
		item.Currency, item.Date = payload.Currency, payload.Date
		if payload.Reference != nil {
			item.Reference = *payload.Reference
		}
		var row reconciliationComparables
		if payload.Category != nil {
			row.Category = *payload.Category
		}
		if payload.VATRate != nil {
			row.VATRate = *payload.VATRate
		}
		if payload.VATAmount != nil {
			row.VATAmount = *payload.VATAmount
		}
		categories[item.ID] = row
		data = append(data, item)
	}
	if err := query.Err(); err != nil {
		return nil, nil, err
	}
	if int64(len(data)) != run.Counts.Rows {
		return nil, nil, unsupportedReconciliationPrepare
	}
	return data, categories, nil
}

func writePrepareGroups(tx *sql.Tx, id int64, groups []reconciliationReferenceGroup, categories map[int64]reconciliationComparables) error {
	groupWrite, err := tx.Prepare("INSERT INTO reconciliation_detail_groups(run_id,ordinal,payload) VALUES($1,$2,$3)")
	if err != nil {
		return err
	}
	defer groupWrite.Close()
	memberWrite, err := tx.Prepare("INSERT INTO reconciliation_detail_members(run_id,group_ordinal,row_id,ordinal) VALUES($1,$2,$3,$4)")
	if err != nil {
		return err
	}
	defer memberWrite.Close()
	for i, group := range groups {
		raw, err := prepareGroupPayload(group, categories)
		if err != nil {
			return err
		}
		if _, err := groupWrite.Exec(id, i+1, string(raw)); err != nil {
			return err
		}
		for _, members := range [][]int64{group.Payments, group.CashFlow} {
			for j, row := range members {
				if _, err := memberWrite.Exec(id, i+1, row, j+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func prepareGroupPayload(group reconciliationReferenceGroup, categories map[int64]reconciliationComparables) ([]byte, error) {
	payload := map[string]any{"kind": group.Kind, "decisions": []any{}}
	if group.Kind == "candidate" {
		left, right := categories[group.Payments[0]], categories[group.CashFlow[0]]
		comparisons := map[string]string{}
		differences := []string{}
		for _, item := range []struct{ name, left, right string }{
			{"category", left.Category, right.Category}, {"vat_rate", left.VATRate, right.VATRate},
			{"vat_amount_minor", left.VATAmount, right.VATAmount},
		} {
			status := "not_comparable"
			if item.left != "" && item.right != "" {
				status = "equal"
				if item.left != item.right {
					status = "different"
					differences = append(differences, item.name)
				}
			}
			comparisons[item.name] = status
		}
		payload["discrepancies"] = differences
		payload["comparisons"] = comparisons
	}
	return json.Marshal(payload)
}
