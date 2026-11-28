package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (a *app) createReconciliationDraft(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	var input struct {
		OutletIDs      []*int64 `json:"outlet_ids"`
		MatchReference *bool    `json:"match_reference"`
	}
	matchReference := false
	input.MatchReference = &matchReference
	if err := parseJSON(w, r, &input); err != nil {
		return err
	}
	if input.OutletIDs == nil || input.MatchReference == nil {
		return badInput
	}
	for _, id := range input.OutletIDs {
		if id == nil {
			return badInput
		}
	}
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := a.lockReconciliationMembership(r, tx, u, entity)
	if err != nil {
		return err
	}
	selected := make(map[int64]bool, len(input.OutletIDs))
	for _, id := range input.OutletIDs {
		selected[*id] = true
	}
	if len(selected) == 0 || len(selected) != len(input.OutletIDs) {
		return problem{422, "Elegí uno o más locales sin repetirlos."}
	}
	allowed, err := reconciliationWritableOutlets(tx, m, selected)
	if err != nil {
		return err
	}
	if len(allowed) != len(selected) {
		return problem{403, "Solo podés conciliar locales autorizados."}
	}
	run, err := a.insertReconciliationDraft(tx, u, entity, allowed, *input.MatchReference)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusCreated, run)
	return nil
}

func (a *app) insertReconciliationDraft(tx *sql.Tx, u user, entity int64, allowed []int64, matchReference bool) (reconciliationReadRun, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var id int64
	if err := tx.QueryRow(`INSERT INTO reconciliation_read_runs
		(entity_id,state,revision,rules_version,match_reference,created_at,created_by_id,updated_at)
		VALUES($1,'draft',1,1,$2,$3,$4,$5) RETURNING id`, entity, matchReference, now, u.id, a.reconciliationRowTime()).Scan(&id); err != nil {
		return reconciliationReadRun{}, err
	}
	for _, outlet := range allowed {
		linked, err := tx.Exec("INSERT INTO reconciliation_read_outlets(run_id,outlet_id) VALUES($1,$2)", id, outlet)
		if err != nil {
			return reconciliationReadRun{}, err
		}
		n, err := linked.RowsAffected()
		if err != nil {
			return reconciliationReadRun{}, err
		}
		if n != 1 {
			return reconciliationReadRun{}, errors.New("native reconciliation outlet was not recorded")
		}
	}
	if _, err := reconciliationWireTime(&now); err != nil {
		return reconciliationReadRun{}, err
	}
	event, err := json.Marshal(map[string]any{
		"action": "create", "actor": u.name, "occurred_at": now, "run_revision": 1,
		"source_id": nil, "payment_row_id": nil, "cash_flow_row_id": nil, "match_id": nil,
	})
	if err != nil {
		return reconciliationReadRun{}, err
	}
	audited, err := tx.Exec("INSERT INTO reconciliation_detail_events(run_id,ordinal,payload,actor_id) VALUES($1,1,$2,$3)", id, string(event), u.id)
	if err != nil {
		return reconciliationReadRun{}, err
	}
	n, err := audited.RowsAffected()
	if err != nil {
		return reconciliationReadRun{}, err
	}
	if n != 1 {
		return reconciliationReadRun{}, errors.New("native reconciliation creation audit was not recorded")
	}
	marked, err := tx.Exec("INSERT INTO reconciliation_native_v1_runs(run_id) VALUES($1)", id)
	if err != nil {
		return reconciliationReadRun{}, err
	}
	n, err = marked.RowsAffected()
	if err != nil {
		return reconciliationReadRun{}, err
	}
	if n != 1 {
		return reconciliationReadRun{}, errors.New("native reconciliation origin was not recorded")
	}
	return reconciliationReadRun{
		ID: id, EntityID: entity, Outlets: allowed, State: "draft", Revision: 1, Rules: 1,
		MatchReference: matchReference, Created: now,
	}, nil
}

func reconciliationWritableOutlets(tx *sql.Tx, m entityMembership, selected map[int64]bool) ([]int64, error) {
	rows, err := tx.Query(`SELECT o.id FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
		WHERE g.membership_id=$1 AND o.entity_id=$2 AND o.active=TRUE ORDER BY o.name,o.id`, m.id, m.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if selected[id] {
			allowed = append(allowed, id)
		}
	}
	return allowed, rows.Err()
}
