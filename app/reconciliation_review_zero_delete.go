package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// Only an ordinary prepare at revision four with two rows and no
// decisions uses this witness. Never infer native origin from
// historical audit; the caller requires the creation marker separately.
func supportedZeroDecisionReviewRemoval(tx *sql.Tx, run reconciliationReadRun, unsupported problem) error {
	if run.Revision != 4 || run.Counts.Sources != 2 || run.Counts.Rows != 2 {
		return unsupported
	}
	if _, err := supportedReconciliationClosure(tx, run); err != nil {
		return err
	}
	normalized, _, err := readPrepareRows(tx, run)
	if err != nil {
		return err
	}
	groups, err := buildReconciliationGroups(normalized, run.MatchReference)
	if err == nil && twoUnmatchedReviewSingletons(groups) {
		_, _, err := supportedBoundedReviewRemoval(tx, run)
		return err
	}
	if err != nil || len(groups) != 1 || groups[0].Kind != "candidate" ||
		len(groups[0].Payments) != 1 || len(groups[0].CashFlow) != 1 {
		return unsupported
	}
	var shaped bool
	err = tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM reconciliation_detail_groups WHERE run_id=$1)=1
		AND EXISTS(SELECT 1 FROM reconciliation_detail_groups WHERE run_id=$1 AND ordinal=1
			AND CAST(payload AS json)->>'kind'='candidate' AND CASE WHEN json_typeof(CAST(payload AS json)->'decisions')='array'
			THEN json_array_length(CAST(payload AS json)->'decisions')=0 ELSE false END)
		AND (SELECT COUNT(*) FROM reconciliation_detail_members WHERE run_id=$1
			AND group_ordinal=1 AND ordinal=1 AND row_id IN ($2,$3))=2`,
		run.ID, groups[0].Payments[0], groups[0].CashFlow[0]).Scan(&shaped)
	if err != nil {
		return err
	}
	if !shaped {
		return unsupported
	}

	type audit struct {
		actor, ordinal int64
		key            sql.NullString
		event          struct {
			Action   string `json:"action"`
			Occurred string `json:"occurred_at"`
			Revision int64  `json:"run_revision"`
			Source   *int64 `json:"source_id"`
			Payment  *int64 `json:"payment_row_id"`
			Cash     *int64 `json:"cash_flow_row_id"`
			Match    *int64 `json:"match_id"`
		}
	}
	timeline, err := tx.Query(`SELECT ordinal,actor_id,operation_key,payload,
		(SELECT COUNT(*)=8 AND COUNT(DISTINCT CASE WHEN key IN (
			'action','actor','occurred_at','run_revision','source_id','payment_row_id','cash_flow_row_id','match_id'
		) THEN key END)=8 FROM json_each(CASE WHEN json_typeof(CAST(payload AS json))='object' THEN CAST(payload AS json) ELSE '{}'::json END))
		FROM reconciliation_detail_events WHERE run_id=$1 ORDER BY ordinal LIMIT 5`, run.ID)
	if err != nil {
		return err
	}
	defer timeline.Close()
	var events [4]audit
	var count int
	for timeline.Next() {
		if count == len(events) {
			return unsupported
		}
		entry := &events[count]
		var actor sql.NullInt64
		var raw string
		var exactFields bool
		if err := timeline.Scan(&entry.ordinal, &actor, &entry.key, &raw, &exactFields); err != nil {
			return err
		}
		if !actor.Valid || !exactFields {
			return unsupported
		}
		entry.actor = actor.Int64
		if err := json.Unmarshal([]byte(raw), &entry.event); err != nil {
			return unsupported
		}
		event := entry.event
		if entry.ordinal != int64(count+1) || event.Revision != entry.ordinal ||
			event.Action != [...]string{"create", "import", "import", "prepare"}[count] ||
			event.Payment != nil || event.Cash != nil || event.Match != nil {
			return unsupported
		}
		if _, err := reconciliationWireTime(&event.Occurred); err != nil {
			return unsupported
		}
		count++
	}
	if err := timeline.Err(); err != nil {
		return err
	}
	if err := timeline.Close(); err != nil {
		return err
	}
	if count != len(events) {
		return unsupported
	}

	validKey := func(key string) bool {
		return key != "" && utf8.ValidString(key) && utf8.RuneCountInString(key) <= 64
	}
	create, prepare := events[0], events[3]
	if create.key.Valid || create.event.Source != nil || create.event.Occurred != run.Created ||
		!prepare.key.Valid || !validKey(prepare.key.String) || prepare.event.Source != nil {
		return unsupported
	}
	var creator sql.NullInt64
	if err := tx.QueryRow("SELECT created_by_id FROM reconciliation_read_runs WHERE id=$1", run.ID).Scan(&creator); err != nil {
		return err
	}
	if !creator.Valid || creator.Int64 != create.actor {
		return unsupported
	}
	var previousSource int64
	var previousSide string
	for _, entry := range events[1:3] {
		if entry.key.Valid || entry.event.Source == nil || *entry.event.Source == previousSource {
			return unsupported
		}
		var importedBy sql.NullInt64
		var sourceKey, side string
		err := tx.QueryRow(`SELECT imported_by_id,operation_key,side FROM reconciliation_read_sources
			WHERE run_id=$1 AND id=$2 AND row_count=1`, run.ID, *entry.event.Source).Scan(&importedBy, &sourceKey, &side)
		if errors.Is(err, sql.ErrNoRows) {
			return unsupported
		}
		if err != nil {
			return err
		}
		if !importedBy.Valid || importedBy.Int64 != entry.actor || !validKey(sourceKey) || side == previousSide {
			return unsupported
		}
		previousSource, previousSide = *entry.event.Source, side
	}
	return nil
}

// The native matcher has already assigned the keys and sides. Only a
// single unmatched payment and a single unmatched cash row qualify.
func twoUnmatchedReviewSingletons(groups []reconciliationReferenceGroup) bool {
	if len(groups) != 2 {
		return false
	}
	var payments, cash int
	for _, group := range groups {
		if group.Kind != "unmatched" || len(group.Payments)+len(group.CashFlow) != 1 {
			return false
		}
		payments += len(group.Payments)
		cash += len(group.CashFlow)
	}
	return payments == 1 && cash == 1
}
