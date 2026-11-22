package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"
)

func (a *app) closeReconciliationReview(w http.ResponseWriter, r *http.Request, u user) error {
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
	var event string
	err = tx.QueryRow(`SELECT (CAST(payload AS json)->>'action') FROM reconciliation_detail_events
		WHERE run_id=$1 AND operation_key=$2`, id, *input.Key).Scan(&event)
	if err == nil {
		if event != "close" {
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
	if run.State != "review" {
		return problem{409, "La conciliación no está en revisión."}
	}
	unresolved, err := supportedReconciliationClosure(tx, run)
	if err != nil {
		return err
	}
	if *input.Revision < 1 {
		return problem{422, "La revisión esperada no es válida."}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	change, err := tx.Exec(`UPDATE reconciliation_read_runs SET state='closed',revision=revision+1,
		closed_at=$1,unresolved_count=$2,updated_at=$3 WHERE id=$4 AND state='review' AND revision=$5`, now, unresolved, a.reconciliationRowTime(), id, *input.Revision)
	if err != nil {
		return err
	}
	count, err := change.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return problem{409, "La conciliación cambió. Recargá la página."}
	}
	run.State = "closed"
	run.Revision++
	if _, err := reconciliationWireTime(&now); err != nil {
		return err
	}
	run.Closed = &now
	run.Unresolved = &unresolved
	payload, err := json.Marshal(map[string]any{
		"action": "close", "actor": u.name, "occurred_at": now, "run_revision": run.Revision,
		"source_id": nil, "payment_row_id": nil, "cash_flow_row_id": nil, "match_id": nil,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO reconciliation_detail_events(run_id,ordinal,payload,actor_id,operation_key)
		VALUES($1,(SELECT COALESCE(MAX(ordinal),0)+1 FROM reconciliation_detail_events WHERE run_id=$2),$3,$4,$5)`,
		id, id, string(payload), u.id, *input.Key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, run)
	return nil
}

func supportedReconciliationClosure(tx *sql.Tx, run reconciliationReadRun) (int64, error) {
	var rows, members, confirmed, rejected int64
	var native bool
	err := tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM reconciliation_detail_rows WHERE run_id=$1),
		(SELECT COUNT(*) FROM reconciliation_detail_members WHERE run_id=$2),
		(SELECT COUNT(*) FROM reconciliation_review_decisions WHERE run_id=$3 AND decision='confirmed'),
		(SELECT COUNT(*) FROM reconciliation_review_decisions WHERE run_id=$4 AND decision='rejected'),
		EXISTS(SELECT 1 FROM reconciliation_detail_events WHERE run_id=$5 AND actor_id IS NOT NULL
			AND operation_key IS NOT NULL AND (CAST(payload AS json)->>'action')='prepare')`,
		run.ID, run.ID, run.ID, run.ID, run.ID).Scan(&rows, &members, &confirmed, &rejected, &native)
	if err != nil {
		return 0, err
	}
	if !native || run.Rules != 1 || rows != run.Counts.Rows || members != rows ||
		confirmed != run.Counts.Confirmed || rejected != run.Counts.Rejected || confirmed > rows/2 {
		return 0, problem{503, "El cierre no está disponible para estos datos de revisión."}
	}
	return rows - 2*confirmed, nil
}
