package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"
)

type reconciliationReviewDecision struct {
	Payment      int64  `json:"payment_row_id"`
	Cash         int64  `json:"cash_flow_row_id"`
	Decision     string `json:"decision"`
	Acknowledged bool   `json:"discrepancy_acknowledged"`
}

func readReconciliationDecisions(tx *sql.Tx, id, group int64) ([]json.RawMessage, error) {
	rows, err := tx.Query(`SELECT payment_row_id,cash_flow_row_id,acknowledged,decision FROM reconciliation_review_decisions
		WHERE run_id=$1 AND group_ordinal=$2 ORDER BY payment_row_id,id`, id, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var value reconciliationReviewDecision
		if err := rows.Scan(&value.Payment, &value.Cash, &value.Acknowledged, &value.Decision); err != nil {
			return nil, err
		}
		out, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		result = append(result, json.RawMessage(out))
	}
	return result, rows.Err()
}

type reconciliationRejectPairRow struct {
	Outlet                            int64
	Date, Amount, Currency, Reference string
	Group                             sql.NullInt64
	Comparables                       reconciliationComparables
}

func reconciliationRejectRow(tx *sql.Tx, id, row int64, side string) (reconciliationRejectPairRow, error) {
	var out reconciliationRejectPairRow
	err := tx.QueryRow(`SELECT r.outlet_id,(CAST(r.payload AS json)->>'date'),(CAST(r.payload AS json)->>'amount'),
		(CAST(r.payload AS json)->>'currency'),COALESCE((CAST(r.payload AS json)->>'reference'),''),g.group_ordinal,
        COALESCE((CAST(r.payload AS json)->>'category'),''),COALESCE((CAST(r.payload AS json)->>'vat_rate'),''),
        COALESCE((CAST(r.payload AS json)->>'vat_amount'),'')
		FROM reconciliation_detail_rows r JOIN reconciliation_read_sources s ON s.id=r.source_id AND s.run_id=r.run_id
		LEFT JOIN reconciliation_detail_members g ON g.run_id=r.run_id AND g.row_id=r.id
		WHERE r.run_id=$1 AND r.id=$2 AND s.side=$3`, id, row, side).Scan(
		&out.Outlet, &out.Date, &out.Amount, &out.Currency, &out.Reference, &out.Group, &out.Comparables.Category, &out.Comparables.VATRate, &out.Comparables.VATAmount)
	if errors.Is(err, sql.ErrNoRows) {
		return out, problem{404, "No se encontró el par de filas."}
	}
	return out, err
}

func (a *app) decideReconciliationPair(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	id, err := pathID(r, "run")
	if err != nil {
		return err
	}
	ack := false
	input := struct {
		Revision     *int64  `json:"expected_revision"`
		Key          *string `json:"operation_key"`
		Decision     *string `json:"decision"`
		Payment      *int64  `json:"payment_row_id"`
		Cash         *int64  `json:"cash_flow_row_id"`
		Acknowledged *bool   `json:"discrepancy_acknowledged"`
	}{Acknowledged: &ack}
	if err := parseJSON(w, r, &input); err != nil {
		return err
	}
	if input.Revision == nil || input.Key == nil || input.Decision == nil || input.Payment == nil || input.Cash == nil || input.Acknowledged == nil {
		return badInput
	}
	if *input.Decision != "rejected" && *input.Decision != "confirmed" {
		return problem{422, "La decisión no es válida."}
	}
	if *input.Key == "" || utf8.RuneCountInString(*input.Key) > 64 {
		return problem{422, "La clave de operación no es válida."}
	}
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := a.lockReconciliationRun(r, tx, u, entity, id, false); err != nil {
		return err
	}
	run, err := readReconciliationCSVScope(tx, u, entity, id, 0)
	if err != nil {
		return err
	}
	var rowID, payment, cash int64
	var recorded bool
	var prior string
	err = tx.QueryRow(`SELECT id,payment_row_id,cash_flow_row_id,acknowledged,decision FROM reconciliation_review_decisions
		WHERE run_id=$1 AND operation_key=$2`, id, *input.Key).Scan(&rowID, &payment, &cash, &recorded, &prior)
	if err == nil {
		if payment != *input.Payment || cash != *input.Cash || recorded != *input.Acknowledged || prior != *input.Decision {
			return importConflict
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, http.StatusOK, map[string]any{"id": rowID, "revision": run.Revision, "replayed": true})
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if run.State != "review" {
		return problem{409, "La conciliación no está en revisión."}
	}
	p, err := reconciliationRejectRow(tx, id, *input.Payment, "payments")
	if err != nil {
		return err
	}
	c, err := reconciliationRejectRow(tx, id, *input.Cash, "cash_flow")
	if err != nil {
		return err
	}
	if p.Outlet != c.Outlet || p.Date != c.Date || p.Amount != c.Amount || p.Currency != c.Currency ||
		(run.MatchReference && (p.Reference == "" || p.Reference != c.Reference)) {
		return problem{422, "Solo se puede decidir un par con coincidencia exacta."}
	}
	var rejected, confirmed int64
	if err := tx.QueryRow(`SELECT COUNT(*) FILTER (WHERE decision='rejected'),COUNT(*) FILTER (WHERE decision='confirmed')
		FROM reconciliation_review_decisions WHERE run_id=$1`, id).Scan(&rejected, &confirmed); err != nil {
		return err
	}
	if !p.Group.Valid || p.Group != c.Group || run.Counts.Confirmed != confirmed || run.Counts.Rejected != rejected {
		return problem{503, "La revisión del par no está disponible para estos datos."}
	}
	if *input.Decision == "confirmed" {
		var payments, cashFlow int
		err := tx.QueryRow(`SELECT COUNT(*) FILTER (WHERE s.side='payments'),COUNT(*) FILTER (WHERE s.side='cash_flow')
			FROM reconciliation_detail_members g JOIN reconciliation_detail_rows r ON r.run_id=g.run_id AND r.id=g.row_id
			JOIN reconciliation_read_sources s ON s.run_id=r.run_id AND s.id=r.source_id
			WHERE g.run_id=$1 AND g.group_ordinal=$2`, id, p.Group.Int64).Scan(&payments, &cashFlow)
		if err != nil {
			return err
		}
		if payments != 1 || cashFlow != 1 {
			return problem{422, "Los grupos ambiguos deben quedar sin confirmar."}
		}
		if !*input.Acknowledged && hasReconciliationDiscrepancy(p.Comparables, c.Comparables) {
			return problem{422, "Confirmá que revisaste las diferencias de categoría o IVA."}
		}
	}
	if *input.Revision < 1 {
		return problem{422, "La revisión esperada no es válida."}
	}
	change, err := tx.Exec(`UPDATE reconciliation_read_runs SET revision=revision+1,rejected_count=rejected_count+CASE WHEN $1='rejected' THEN 1 ELSE 0 END,
        confirmed_count=confirmed_count+CASE WHEN $2='confirmed' THEN 1 ELSE 0 END,updated_at=$3 WHERE id=$4 AND state='review' AND revision=$5`,
		*input.Decision, *input.Decision, a.reconciliationRowTime(), id, *input.Revision)
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
	var matchID int64
	err = tx.QueryRow(`INSERT INTO reconciliation_review_decisions
		(run_id,group_ordinal,payment_row_id,cash_flow_row_id,acknowledged,operation_key,actor_id,decision,decided_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, id, p.Group.Int64, *input.Payment, *input.Cash, *input.Acknowledged, *input.Key, u.id, *input.Decision, a.reconciliationRowTime()).Scan(&matchID)
	if err != nil {
		if isUnique(err) {
			return problem{409, "Una de las filas ya fue confirmada o el par ya fue decidido."}
		}
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := reconciliationWireTime(&now); err != nil {
		return err
	}
	event, err := json.Marshal(map[string]any{
		"action": *input.Decision, "actor": u.name, "occurred_at": now, "run_revision": *input.Revision + 1,
		"source_id": nil, "payment_row_id": *input.Payment, "cash_flow_row_id": *input.Cash, "match_id": matchID,
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
	jsonResponse(w, http.StatusOK, map[string]any{"id": matchID, "revision": *input.Revision + 1, "decision": *input.Decision})
	return nil
}

func hasReconciliationDiscrepancy(left, right reconciliationComparables) bool {
	for _, values := range [][2]string{{left.Category, right.Category}, {left.VATRate, right.VATRate}, {left.VATAmount, right.VATAmount}} {
		if values[0] != "" && values[1] != "" && values[0] != values[1] {
			return true
		}
	}
	return false
}
