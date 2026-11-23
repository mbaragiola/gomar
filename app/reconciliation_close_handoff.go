package main

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

var missingReconciliationCloseHandoff = problem{404, "No se encontró una conciliación en revisión disponible en ese local."}

const singleOutletReconciliation = ` AND (SELECT COUNT(*) FROM reconciliation_read_outlets ro WHERE ro.run_id=r.id)=1`

type reconciliationCloseHandoffView struct {
	RunID int64  `json:"run_id"`
	State string `json:"state"`
	mcpclient.ReconciliationCloseEligibility
}

func (a *app) reconciliationCloseHandoff(w http.ResponseWriter, r *http.Request, u user) error {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !a.enableMCP || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || len(r.Header.Values("Authorization")) != 0 {
		return missingReconciliationCloseHandoff
	}
	entity, entityOK := attachmentHandoffID(r, "entity")
	outlet, outletOK := attachmentHandoffID(r, "outlet")
	run, runOK := attachmentHandoffID(r, "run")
	if !entityOK || !outletOK || !runOK {
		return missingReconciliationCloseHandoff
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entity, "reconcile_financial_data")
	if err != nil {
		if err == missingEntity || err == forbiddenRole {
			return missingReconciliationCloseHandoff
		}
		return err
	}
	var scopedID int64
	err = tx.QueryRowContext(ctx, `SELECT r.id`+mcpReconciliationRunScope+singleOutletReconciliation+
		` AND r.id=$5 AND r.state='review'`, member.ID, outlet, member.id, true, run).Scan(&scopedID)
	if errors.Is(err, sql.ErrNoRows) {
		return missingReconciliationCloseHandoff
	}
	if err != nil {
		return err
	}
	runs, err := scanReconciliationRuns(tx, member, scopedID)
	if err != nil {
		return err
	}
	if len(runs) != 1 {
		return missingReconciliationCloseHandoff
	}
	result, err := reconciliationEligibilityResult(tx, runs[0])
	if err != nil {
		return err
	}
	if result.Blocker == "not_in_review" {
		return missingReconciliationCloseHandoff
	}
	jsonResponse(w, http.StatusOK, reconciliationCloseHandoffView{RunID: scopedID, State: "review", ReconciliationCloseEligibility: result})
	return nil
}
