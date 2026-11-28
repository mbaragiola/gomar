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

type reconciliationDeleteResponse struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	Revision int64  `json:"revision"`
	Replayed bool   `json:"replayed,omitempty"`
}

func (a *app) deleteReconciliationDraft(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	id, err := pathID(r, "run")
	if err != nil {
		return err
	}
	q := r.URL.Query()
	revision, err := strconv.ParseInt(lastReconciliationQuery(q["expected_revision"]), 10, 64)
	if err != nil {
		return badInput
	}
	key := lastReconciliationQuery(q["operation_key"])
	if key == "" || utf8.RuneCountInString(key) > 64 {
		return problem{422, "La clave de operación no es válida."}
	}
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := a.lockReconciliationRun(r, tx, u, entity, id, true); err != nil {
		return err
	}
	m, err := scopedMembership(tx, u.id, entity, "reconcile_financial_data")
	if err != nil {
		return err
	}
	runs, err := scanReconciliationRunsIncludingState(tx, m, id, true)
	if err != nil {
		return err
	}
	missing := problem{404, "No se encontró la conciliación."}
	if len(runs) == 0 {
		return missing
	}
	run := runs[0]
	var prior string
	err = tx.QueryRow(`SELECT (CAST(payload AS json)->>'action')
		FROM reconciliation_detail_events WHERE run_id=$1 AND operation_key=$2`, id, key).Scan(&prior)
	if err == nil {
		if prior != "delete" {
			return problem{409, "La clave de operación ya se usó para otra acción."}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, http.StatusOK, reconciliationDeleteResponse{id, run.State, run.Revision, true})
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if run.State == "deleted" {
		return missing
	}
	const unsupportedDraft = "Solo está disponible la cancelación de conciliaciones en borrador sin preparar."
	prepared := run.State == "review" || run.State == "closed"
	if !prepared && (run.State != "draft" || run.Counts.Confirmed != 0 || run.Counts.Rejected != 0) {
		return problem{503, unsupportedDraft}
	}
	var groups, decisions, incompleteSources int
	if needsBoundedReviewRemoval(run) {
		groups, decisions, err = supportedBoundedReviewRemoval(tx, run)
		if err != nil {
			return err
		}
	} else {
		err = tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM reconciliation_detail_groups WHERE run_id=$1),
		(SELECT COUNT(*) FROM reconciliation_review_decisions WHERE run_id=$2),
		(SELECT COUNT(*) FROM reconciliation_read_sources s WHERE s.run_id=$3
			AND (s.row_count <= 0 OR NULLIF(s.operation_key,'') IS NULL OR s.imported_by_id IS NULL
				OR s.row_count <> (SELECT COUNT(*) FROM reconciliation_detail_rows r
					WHERE r.run_id=s.run_id AND r.source_id=s.id)))`, id, id, id).
			Scan(&groups, &decisions, &incompleteSources)
		if err != nil {
			return err
		}
		switch run.State {
		case "draft":
			if groups != 0 || decisions != 0 || incompleteSources != 0 {
				return problem{503, unsupportedDraft}
			}
		case "closed", "review":
			if err := supportedPreparedReconciliationRemoval(tx, run, incompleteSources); err != nil {
				return err
			}
		}
	}
	if revision < 1 {
		return problem{422, "La revisión esperada no es válida."}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	change, err := tx.Exec(`UPDATE reconciliation_read_runs
		SET state='deleted',deleted_at=$1,unresolved_count=NULL,confirmed_count=0,rejected_count=0,revision=revision+1,updated_at=$2
		WHERE id=$3 AND entity_id=$4 AND state=$5 AND revision=$6`, now, a.reconciliationRowTime(), id, entity, run.State, revision)
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
	// Audit rows refer to the run and carry historical identifiers in their
	// payload; no audit row or scope FK needs to be changed. Delete only this
	// run's projections in FK order; leave all ID high-waters intact.
	type inventory struct {
		query string
		count int64
	}
	children := []inventory{}
	if prepared {
		children = append(children,
			inventory{"DELETE FROM reconciliation_review_decisions WHERE run_id=$1", int64(decisions)},
			inventory{"DELETE FROM reconciliation_detail_members WHERE run_id=$1", run.Counts.Rows},
			inventory{"DELETE FROM reconciliation_detail_groups WHERE run_id=$1", int64(groups)},
		)
	}
	children = append(children,
		inventory{"DELETE FROM reconciliation_detail_rows WHERE run_id=$1", run.Counts.Rows},
		inventory{"DELETE FROM reconciliation_read_sources WHERE run_id=$1", run.Counts.Sources},
	)
	for _, child := range children {
		result, err := tx.Exec(child.query, id)
		if err != nil {
			return err
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if removed != child.count {
			return errors.New("reconciliation removal inventory changed")
		}
	}
	if _, err := reconciliationWireTime(&now); err != nil {
		return err
	}
	event, err := json.Marshal(map[string]any{
		"action": "delete", "actor": u.name, "occurred_at": now, "run_revision": revision + 1,
		"source_id": nil, "payment_row_id": nil, "cash_flow_row_id": nil, "match_id": nil,
	})
	if err != nil {
		return err
	}
	audited, err := tx.Exec(`INSERT INTO reconciliation_detail_events(run_id,ordinal,payload,actor_id,operation_key)
		VALUES($1,(SELECT COALESCE(MAX(ordinal),0)+1 FROM reconciliation_detail_events WHERE run_id=$2),$3,$4,$5)`,
		id, id, string(event), u.id, key)
	if err != nil {
		return err
	}
	events, err := audited.RowsAffected()
	if err != nil {
		return err
	}
	if events != 1 {
		return errors.New("reconciliation removal audit was not recorded")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, reconciliationDeleteResponse{id, "deleted", revision + 1, false})
	return nil
}

func supportedPreparedReconciliationRemoval(tx *sql.Tx, run reconciliationReadRun, incompleteSources int) error {
	unsupported := problem{503, "La eliminación no está disponible para estos datos de conciliación cerrada."}
	review := run.State == "review"
	zeroDecisions := review && run.Counts.Confirmed == 0 && run.Counts.Rejected == 0
	twoDecisions := review && run.Counts.Confirmed == 1 && run.Counts.Rejected == 1
	if review {
		unsupported = problem{503, "La eliminación no está disponible para estos datos de conciliación en revisión."}
		if run.Closed != nil || run.Unresolved != nil || (!zeroDecisions && !twoDecisions && run.Counts.Confirmed+run.Counts.Rejected != 1) {
			return unsupported
		}
	} else if run.Closed == nil || run.Unresolved == nil {
		return unsupported
	}
	if run.Rules != 1 || run.Deleted != nil || incompleteSources != 0 {
		return unsupported
	}
	// Origin is a separate capability set only by native v1 creation.
	// Timestamps and audit rows are integrity checks, not provenance:
	// imported history can faithfully reproduce both.
	var native bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM reconciliation_native_v1_runs WHERE run_id=$1)", run.ID).Scan(&native); err != nil {
		return err
	}
	if !native {
		return unsupported
	}
	if zeroDecisions {
		return supportedZeroDecisionReviewRemoval(tx, run, unsupported)
	}
	if twoDecisions {
		return supportedTwoDecisionReviewRemoval(tx, run, unsupported)
	}
	// Require an attributed terminal close or the exact last native
	// decision in review. The latter stores the key on the decision row,
	// not on the audit event itself.
	var occurredAt string
	var orphanAudit bool
	err := tx.QueryRow(`SELECT (CAST(e.payload AS json)->>'occurred_at'),
		EXISTS(SELECT 1 FROM reconciliation_review_decisions d WHERE d.run_id=e.run_id AND NOT EXISTS (
			SELECT 1 FROM reconciliation_detail_events prior
			WHERE prior.run_id=d.run_id AND prior.ordinal<=e.ordinal AND prior.actor_id=d.actor_id
			AND (CAST(prior.payload AS json)->>'action')=d.decision
			AND (CAST(prior.payload AS jsonb)->'match_id')=to_jsonb(d.id)
			AND (CAST(prior.payload AS jsonb)->'payment_row_id')=to_jsonb(d.payment_row_id)
			AND (CAST(prior.payload AS jsonb)->'cash_flow_row_id')=to_jsonb(d.cash_flow_row_id)
		)) OR EXISTS(SELECT 1 FROM reconciliation_read_sources s WHERE s.run_id=e.run_id AND NOT EXISTS (
			SELECT 1 FROM reconciliation_detail_events prior
			WHERE prior.run_id=s.run_id AND prior.ordinal<e.ordinal AND prior.actor_id=s.imported_by_id
			AND (CAST(prior.payload AS json)->>'action')='import'
			AND (CAST(prior.payload AS jsonb)->'source_id')=to_jsonb(s.id)
		))
		FROM reconciliation_detail_events e WHERE e.run_id=$1
		AND e.ordinal=(SELECT MAX(ordinal) FROM reconciliation_detail_events WHERE run_id=e.run_id)
		AND e.actor_id IS NOT NULL
		AND json_typeof(CAST(e.payload AS json)->'occurred_at')='string'
		AND (CAST(e.payload AS jsonb)->'run_revision')=to_jsonb(CAST($2 AS bigint))
		AND CASE WHEN $3 THEN
			e.operation_key IS NULL
			AND EXISTS(SELECT 1 FROM reconciliation_review_decisions d WHERE d.run_id=e.run_id
				AND d.actor_id=e.actor_id AND to_jsonb(d.id)=(CAST(e.payload AS jsonb)->'match_id')
				AND NULLIF(d.operation_key,'') IS NOT NULL
				AND d.decision=(CAST(e.payload AS json)->>'action')
				AND to_jsonb(d.payment_row_id)=(CAST(e.payload AS jsonb)->'payment_row_id')
				AND to_jsonb(d.cash_flow_row_id)=(CAST(e.payload AS jsonb)->'cash_flow_row_id'))
			AND EXISTS(SELECT 1 FROM reconciliation_detail_events p
				WHERE p.run_id=e.run_id AND p.ordinal=e.ordinal-1 AND p.actor_id IS NOT NULL
				AND NULLIF(p.operation_key,'') IS NOT NULL AND (CAST(p.payload AS json)->>'action')='prepare'
				AND (CAST(p.payload AS jsonb)->'run_revision')=to_jsonb(CAST($4 AS bigint)-1))
			AND (SELECT COUNT(DISTINCT side) FROM reconciliation_read_sources s WHERE s.run_id=e.run_id)=2
			AND NOT EXISTS(SELECT 1 FROM reconciliation_detail_groups g WHERE g.run_id=e.run_id AND NOT EXISTS (
				SELECT 1 FROM reconciliation_detail_members m WHERE m.run_id=g.run_id AND m.group_ordinal=g.ordinal))
		ELSE NULLIF(e.operation_key,'') IS NOT NULL AND (CAST(e.payload AS json)->>'action')='close' END`,
		run.ID, run.Revision, review, run.Revision).Scan(&occurredAt, &orphanAudit)
	if errors.Is(err, sql.ErrNoRows) {
		return unsupported
	}
	if err != nil {
		return err
	}
	if orphanAudit || (!review && occurredAt != *run.Closed) {
		return unsupported
	}
	if _, err := reconciliationWireTime(&occurredAt); err != nil {
		return unsupported
	}
	unresolved, err := supportedReconciliationClosure(tx, run)
	if err != nil {
		return err
	}
	if !review && *run.Unresolved != unresolved {
		return unsupported
	}
	return nil
}

func lastReconciliationQuery(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}
