package main

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func mcpReadReconciliationSetup(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, badInput
	}
	outlet, err := mcpReconciliationOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	run, err := pathID(r, "run")
	if err != nil || run <= 0 {
		return nil, mcpMissingReconciliation
	}
	var out mcpclient.ReconciliationSetup
	// Source config only: look at mapping key presence, never its value or
	// a source's rows. These are setup facts, not a native prepare dry-run.
	err = tx.QueryRowContext(r.Context(), `SELECT r.id,r.revision,r.state,r.match_reference,
 EXISTS(SELECT 1 FROM reconciliation_read_sources x WHERE x.run_id=r.id AND x.side='payments' AND x.row_count>0),
 EXISTS(SELECT 1 FROM reconciliation_read_sources x WHERE x.run_id=r.id AND x.side='cash_flow' AND x.row_count>0),
 (r.match_reference AND EXISTS(SELECT 1 FROM reconciliation_read_sources x WHERE x.run_id=r.id AND json_typeof(x.mapping::json->'reference') IS NULL)),
 (r.rules_version<>1 OR EXISTS(SELECT 1 FROM reconciliation_read_sources x
   WHERE x.run_id=r.id AND (x.operation_key IS NULL OR x.imported_by_id IS NULL OR x.row_count<=0))),
 (SELECT coalesce(sum(x.row_count),0)>20000 FROM reconciliation_read_sources x WHERE x.run_id=r.id)`+
		mcpReconciliationRunScope+` AND r.id=$5`, s.ID, outlet, s.id, s.outlet.Valid, run).
		Scan(&out.ID, &out.Revision, &out.Status, &out.MatchReference, &out.HasPaymentsSource, &out.HasCashFlowSource,
			&out.ReferenceMappingIncomplete, &out.UnsupportedSourceSetup, &out.OverRowLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, mcpMissingReconciliation
	}
	return out, err
}
