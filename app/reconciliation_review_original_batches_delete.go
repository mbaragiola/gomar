package main

import (
	"database/sql"
	"errors"
	"fmt"
)

const maxOriginalReviewSources = 10

// Reconstruct the bounded original-batch history. Source IDs may have gaps
// from an unrelated run importing between these originals.
func checkOriginalBatchReview(tx *sql.Tx, run reconciliationReadRun, unsupported problem) (int, error) {
	type originalSource struct {
		boundedReviewSource
		label string
	}
	var creator sql.NullInt64
	err := tx.QueryRow(`SELECT r.created_by_id FROM reconciliation_read_runs r
		JOIN reconciliation_native_v1_runs n ON n.run_id=r.id WHERE r.id=$1`, run.ID).Scan(&creator)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !creator.Valid) {
		return 0, unsupported
	}
	if err != nil {
		return 0, err
	}
	sources, err := scanBoundedReview(tx, `SELECT id,row_count,imported_by_id,operation_key,side,label
		FROM reconciliation_read_sources WHERE run_id=$1 ORDER BY id LIMIT $2`,
		func(rows *sql.Rows) (originalSource, error) {
			var source originalSource
			err := rows.Scan(&source.id, &source.rows, &source.actor, &source.key, &source.side, &source.label)
			return source, err
		}, run.ID, maxOriginalReviewSources+1)
	if err != nil {
		return 0, err
	}
	if int64(len(sources)) != run.Counts.Sources {
		return 0, unsupported
	}
	sourceByID := make(map[int64]originalSource, len(sources))
	keys := make(map[string]bool, len(sources))
	payments, cash := 0, 0
	for _, source := range sources {
		if source.id < 1 || source.rows < 1 || source.rows > run.Counts.Rows ||
			!source.actor.Valid || source.actor.Int64 < 1 ||
			!source.key.Valid || !boundedReviewKey(source.key.String) || keys[source.key.String] {
			return 0, unsupported
		}
		label := ""
		switch source.side {
		case "payments":
			payments++
			label = fmt.Sprintf("Pagos %d", payments)
		case "cash_flow":
			cash++
			label = fmt.Sprintf("Caja %d", cash)
		}
		if label == "" || source.label != label {
			return 0, unsupported
		}
		keys[source.key.String], sourceByID[source.id] = true, source
	}
	if payments == 0 || cash == 0 {
		return 0, unsupported
	}
	links, err := scanBoundedReview(tx, `SELECT source_id FROM reconciliation_detail_rows
		WHERE run_id=$1 ORDER BY id LIMIT $2`, func(rows *sql.Rows) (int64, error) {
		var id int64
		err := rows.Scan(&id)
		return id, err
	}, run.ID, run.Counts.Rows+1)
	if err != nil {
		return 0, err
	}
	if int64(len(links)) != run.Counts.Rows {
		return 0, unsupported
	}
	actual := make(map[int64]int64, len(sources))
	for _, id := range links {
		if _, exists := sourceByID[id]; !exists {
			return 0, unsupported
		}
		actual[id]++
	}
	for _, source := range sources {
		if actual[source.id] != source.rows {
			return 0, unsupported
		}
	}
	normalized, comparables, err := readPrepareRows(tx, run)
	if err != nil {
		return 0, err
	}
	groups, err := buildReconciliationGroups(normalized, run.MatchReference)
	if err != nil || int64(len(groups))*2 != run.Counts.Rows || len(groups) > boundedReviewGroups {
		return 0, unsupported
	}
	for _, group := range groups {
		if group.Kind != "candidate" || len(group.Payments) != 1 || len(group.CashFlow) != 1 {
			return 0, unsupported
		}
	}
	if err := checkExactReviewProjection(tx, run.ID, groups, comparables, len(normalized), run.MatchReference, unsupported); err != nil {
		return 0, err
	}
	var decisions map[int64]boundedReviewDecision
	if run.Counts.Confirmed+run.Counts.Rejected == 0 {
		var exists bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM reconciliation_review_decisions WHERE run_id=$1)", run.ID).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			return 0, unsupported
		}
	} else {
		pairs := make(map[int64]boundedReviewPair, len(groups))
		for i, g := range groups {
			pairs[int64(i+1)] = boundedReviewPair{g.Payments[0], g.CashFlow[0]}
		}
		decisions, err = readBoundedReviewDecisions(tx, run, pairs, comparables, unsupported)
		if err != nil {
			return 0, err
		}
	}
	preparedIndex := len(sources) + 1
	expected := preparedIndex + 1 + len(decisions)
	events, err := readTypedReviewAudit(tx, run.ID, expected+1)
	if err != nil {
		return 0, err
	}
	if len(events) != expected {
		return 0, unsupported
	}
	var lastDecision int64
	for i, entry := range events {
		e := entry.event
		if !entry.strict || !entry.actor.Valid || entry.actor.Int64 < 1 || e.Actor == nil || entry.ordinal != int64(i+1) ||
			e.Revision != entry.ordinal ||
			(i <= preparedIndex && (e.Payment != nil || e.Cash != nil || e.Match != nil)) {
			return 0, unsupported
		}
		if _, err := reconciliationWireTime(&e.Occurred); err != nil {
			return 0, unsupported
		}
		switch {
		case i == 0:
			if e.Action != "create" || e.Source != nil || entry.key.Valid || entry.actor.Int64 != creator.Int64 || e.Occurred != run.Created {
				return 0, unsupported
			}
		case i <= len(sources):
			source := sources[i-1]
			if e.Action != "import" || e.Source == nil || *e.Source != source.id || entry.key.Valid || entry.actor.Int64 != source.actor.Int64 {
				return 0, unsupported
			}
		case i == preparedIndex:
			if e.Action != "prepare" || e.Source != nil || !entry.key.Valid || !boundedReviewKey(entry.key.String) {
				return 0, unsupported
			}
		default:
			if entry.key.Valid || e.Source != nil || e.Payment == nil || e.Cash == nil ||
				e.Match == nil || *e.Match <= lastDecision {
				return 0, unsupported
			}
			decision, exists := decisions[*e.Match]
			if !exists || decision.actor != entry.actor.Int64 || decision.payment != *e.Payment ||
				decision.cash != *e.Cash || decision.action != e.Action {
				return 0, unsupported
			}
			lastDecision = *e.Match
		}
	}
	return len(groups), nil
}
