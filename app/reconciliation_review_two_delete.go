package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// This witness applies only to the bounded two-decision review case,
// after the parent removal has verified scope, state and native origin.
func supportedTwoDecisionReviewRemoval(tx *sql.Tx, run reconciliationReadRun, unsupported problem) error {
	if _, err := supportedReconciliationClosure(tx, run); err != nil {
		return err
	}
	type audit struct {
		ordinal int64
		actor   sql.NullInt64
		key     sql.NullString
		event   struct {
			Action   string `json:"action"`
			Occurred string `json:"occurred_at"`
			Revision int64  `json:"run_revision"`
			Source   *int64 `json:"source_id"`
			Payment  *int64 `json:"payment_row_id"`
			Cash     *int64 `json:"cash_flow_row_id"`
			Match    *int64 `json:"match_id"`
		}
	}
	var suffix [3]audit
	rows, err := tx.Query(`SELECT ordinal,actor_id,operation_key,payload,
		(SELECT COUNT(*)=8 AND COUNT(DISTINCT CASE WHEN key IN (
			'action','actor','occurred_at','run_revision','source_id','payment_row_id','cash_flow_row_id','match_id'
		) THEN key END)=8 FROM json_each(CASE WHEN json_typeof(CAST(payload AS json))='object' THEN CAST(payload AS json) ELSE '{}'::json END))
		FROM reconciliation_detail_events WHERE run_id=$1 ORDER BY ordinal DESC LIMIT 3`, run.ID)
	if err != nil {
		return err
	}
	var count int
	for rows.Next() {
		entry := &suffix[count]
		var payload string
		var exactFields bool
		if err := rows.Scan(&entry.ordinal, &entry.actor, &entry.key, &payload, &exactFields); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(payload), &entry.event); err != nil || !exactFields {
			rows.Close()
			return unsupported
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if count != len(suffix) {
		return unsupported
	}
	validKey := func(key string) bool {
		return key != "" && utf8.ValidString(key) && utf8.RuneCountInString(key) <= 64
	}
	for i, entry := range suffix {
		if entry.ordinal != suffix[0].ordinal-int64(i) || entry.event.Revision != run.Revision-int64(i) ||
			!entry.actor.Valid || entry.event.Source != nil || entry.event.Occurred == "" {
			return unsupported
		}
		if _, err := reconciliationWireTime(&entry.event.Occurred); err != nil {
			return unsupported
		}
	}
	prepare := suffix[2]
	if prepare.event.Action != "prepare" || !prepare.key.Valid || !validKey(prepare.key.String) ||
		prepare.event.Payment != nil || prepare.event.Cash != nil || prepare.event.Match != nil {
		return unsupported
	}
	var validHistory bool
	err = tx.QueryRow(`SELECT
		(SELECT COUNT(DISTINCT side) FROM reconciliation_read_sources WHERE run_id=$1)=2
		AND NOT EXISTS(SELECT 1 FROM reconciliation_read_sources s WHERE s.run_id=$1 AND NOT EXISTS (
			SELECT 1 FROM reconciliation_detail_events e WHERE e.run_id=s.run_id AND e.ordinal<$2
			AND e.actor_id=s.imported_by_id AND (CAST(e.payload AS json)->>'action')='import'
			AND (CAST(e.payload AS jsonb)->'source_id')=to_jsonb(s.id)))
		AND (SELECT COUNT(*) FROM reconciliation_detail_groups WHERE run_id=$1)=2
		AND NOT EXISTS(SELECT 1 FROM reconciliation_detail_events e WHERE e.run_id=$1 AND e.ordinal<$2
			AND COALESCE((CAST(e.payload AS json)->>'action'),'') NOT IN ('create','import','remove_source'))`,
		run.ID, prepare.ordinal).Scan(&validHistory)
	if err != nil {
		return err
	}
	if !validHistory {
		return unsupported
	}
	var match, payment, cash, group int64
	var decision, key string
	for i, entry := range suffix[:2] {
		event := entry.event
		if entry.key.Valid || event.Match == nil || event.Payment == nil || event.Cash == nil ||
			(event.Action != "confirmed" && event.Action != "rejected") || *event.Payment == *event.Cash {
			return unsupported
		}
		var currentGroup, members int64
		var currentKey string
		err := tx.QueryRow(`SELECT d.group_ordinal,d.operation_key,
			(SELECT COUNT(*) FROM reconciliation_detail_members m WHERE m.run_id=d.run_id AND m.group_ordinal=d.group_ordinal)
			FROM reconciliation_review_decisions d
			WHERE d.run_id=$1 AND d.id=$2 AND d.actor_id=$3 AND d.decision=$4 AND d.payment_row_id=$5 AND d.cash_flow_row_id=$6`,
			run.ID, *event.Match, entry.actor.Int64, event.Action, *event.Payment, *event.Cash).
			Scan(&currentGroup, &currentKey, &members)
		if errors.Is(err, sql.ErrNoRows) {
			return unsupported
		}
		if err != nil {
			return err
		}
		if members != 2 || !validKey(currentKey) || (i == 1 &&
			(*event.Match == match || *event.Payment == payment || *event.Cash == cash ||
				*event.Payment == cash || *event.Cash == payment ||
				currentGroup == group || event.Action == decision || currentKey == key)) {
			return unsupported
		}
		left, err := reconciliationRejectRow(tx, run.ID, *event.Payment, "payments")
		if err != nil {
			var scoped problem
			if errors.As(err, &scoped) {
				return unsupported
			}
			return err
		}
		right, err := reconciliationRejectRow(tx, run.ID, *event.Cash, "cash_flow")
		if err != nil {
			var scoped problem
			if errors.As(err, &scoped) {
				return unsupported
			}
			return err
		}
		if !left.Group.Valid || left.Group != right.Group || left.Group.Int64 != currentGroup ||
			left.Outlet != right.Outlet || left.Date != right.Date || left.Amount != right.Amount ||
			left.Currency != right.Currency || (run.MatchReference && (left.Reference == "" || left.Reference != right.Reference)) {
			return unsupported
		}
		match, payment, cash, group, decision, key = *event.Match, *event.Payment, *event.Cash, currentGroup, event.Action, currentKey
	}
	return nil
}
