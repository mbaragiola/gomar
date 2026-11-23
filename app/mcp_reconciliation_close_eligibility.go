package main

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func mcpReadReconciliationCloseEligibility(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpReconciliationOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, badInput
	}
	id, err := pathID(r, "run")
	if err != nil || id <= 0 {
		return nil, mcpMissingReconciliation
	}
	// The MCP token may be narrower than the membership. Verify the requested
	// outlet and every current participant before using the native run loader.
	var scopedID int64
	err = tx.QueryRowContext(r.Context(), `SELECT r.id`+mcpReconciliationRunScope+
		` AND r.id=$5`, s.ID, outlet, s.id, s.outlet.Valid, id).Scan(&scopedID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, mcpMissingReconciliation
	}
	if err != nil {
		return nil, err
	}
	runs, err := scanReconciliationRuns(tx, s.entityMembership, scopedID)
	if err != nil {
		return nil, err
	}
	if len(runs) != 1 {
		return nil, mcpMissingReconciliation
	}
	_, err = reconciliationClosureEligibility(tx, runs[0])
	switch {
	case err == nil:
		return mcpclient.ReconciliationCloseEligibility{Eligible: true}, nil
	case errors.Is(err, reconciliationNotInReview):
		return mcpclient.ReconciliationCloseEligibility{Blocker: "not_in_review"}, nil
	case errors.Is(err, unsupportedReconciliationReviewData):
		return mcpclient.ReconciliationCloseEligibility{Blocker: "unsupported_review_data"}, nil
	default:
		return nil, err
	}
}
