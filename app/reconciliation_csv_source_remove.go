package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

func (a *app) removeReconciliationSource(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	runID, err := pathID(r, "run")
	if err != nil {
		return err
	}
	sourceID, err := pathID(r, "source")
	if err != nil {
		return err
	}
	query := r.URL.Query()
	expected, err := strconv.ParseInt(lastReconciliationQuery(query["expected_revision"]), 10, 64)
	if err != nil {
		return badInput
	}
	key := lastReconciliationQuery(query["operation_key"])
	if key == "" || utf8.RuneCountInString(key) > 64 {
		return problem{422, "La clave de operación no es válida."}
	}

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockReconciliationRun(tx, u, entity, runID, false); err != nil {
		return err
	}
	run, err := readReconciliationCSVScope(tx, u, entity, runID, 0)
	if err != nil {
		return err
	}
	var action string
	var priorSource sql.NullInt64
	err = tx.QueryRow(`SELECT (CAST(payload AS json)->>'action'),(CAST(payload AS json)->>'source_id')
		FROM reconciliation_detail_events WHERE run_id=$1 AND operation_key=$2`, runID, key).Scan(&action, &priorSource)
	if err == nil {
		if action != "delete_source" || !priorSource.Valid || priorSource.Int64 != sourceID {
			return importConflict
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, http.StatusOK, map[string]any{"revision": run.Revision, "replayed": true})
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if run.State != "draft" {
		return problem{409, "La conciliación ya no admite cambios de archivos."}
	}
	var present bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM reconciliation_read_sources WHERE id=$1 AND run_id=$2)", sourceID, runID).
		Scan(&present); err != nil {
		return err
	}
	if !present {
		return problem{404, "No se encontró el archivo importado."}
	}
	if expected < 1 {
		return problem{422, "La revisión esperada no es válida."}
	}
	change, err := tx.Exec("UPDATE reconciliation_read_runs SET revision=revision+1,updated_at=$1 WHERE id=$2 AND state='draft' AND revision=$3", a.reconciliationRowTime(), runID, expected)
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := reconciliationWireTime(&now); err != nil {
		return err
	}
	event, err := json.Marshal(map[string]any{
		"action": "delete_source", "actor": u.name, "occurred_at": now, "run_revision": expected + 1, "source_id": sourceID,
		"payment_row_id": nil, "cash_flow_row_id": nil, "match_id": nil,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO reconciliation_detail_events(run_id,ordinal,payload,actor_id,operation_key)
		VALUES($1,(SELECT COALESCE(MAX(ordinal),0)+1 FROM reconciliation_detail_events WHERE run_id=$2),$3,$4,$5)`,
		runID, runID, string(event), u.id, key); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM reconciliation_detail_rows WHERE run_id=$1 AND source_id=$2", runID, sourceID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM reconciliation_read_sources WHERE run_id=$1 AND id=$2", runID, sourceID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, map[string]any{"revision": expected + 1})
	return nil
}
