package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

const boundedReviewGroups = 128

// Preserve the behavior of the accepted narrow review paths. Only the
// additional shapes use this stricter, bounded original-history witness.
func needsBoundedReviewRemoval(run reconciliationReadRun) bool {
	if run.State != "review" {
		return false
	}
	c, r := run.Counts.Confirmed, run.Counts.Rejected
	return !((c == 0 && r == 0 && run.Counts.Rows == 2) ||
		(c == 1 && r == 0) || (c == 0 && r == 1) ||
		(c == 1 && r == 1 && run.Counts.Rows == 4))
}

type boundedReviewSource struct {
	id, rows int64
	actor    sql.NullInt64
	key      sql.NullString
	side     string
}

type boundedReviewPair struct{ payment, cash int64 }

func (p boundedReviewPair) matched() bool { return p.payment != 0 && p.cash != 0 }

type boundedReviewDecision struct {
	id, group, payment, cash, actor int64
	key, action                     string
	acknowledged                    bool
}

type boundedReviewAudit struct {
	ordinal int64
	actor   sql.NullInt64
	key     sql.NullString
	strict  bool
	event   struct {
		Action   string  `json:"action"`
		Actor    *string `json:"actor"`
		Occurred string  `json:"occurred_at"`
		Revision int64   `json:"run_revision"`
		Source   *int64  `json:"source_id"`
		Payment  *int64  `json:"payment_row_id"`
		Cash     *int64  `json:"cash_flow_row_id"`
		Match    *int64  `json:"match_id"`
	}
}

func scanBoundedReview[T any](tx *sql.Tx, query string, decode func(*sql.Rows) (T, error), args ...any) ([]T, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		value, err := decode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func boundedReviewKey(key string) bool {
	return key != "" && utf8.ValidString(key) && utf8.RuneCountInString(key) <= 64
}

func supportedBoundedReviewRemoval(tx *sql.Tx, run reconciliationReadRun) (int, int, error) {
	unsupported := problem{503, "La eliminación no está disponible para estos datos de conciliación en revisión."}
	confirmed, rejected := run.Counts.Confirmed, run.Counts.Rejected
	if run.State == "review" && run.Rules == 1 &&
		run.Closed == nil && run.Deleted == nil && run.Unresolved == nil &&
		run.Counts.Sources >= 3 && run.Counts.Sources <= maxOriginalReviewSources &&
		run.Counts.Rows >= run.Counts.Sources && run.Counts.Rows <= 2*boundedReviewGroups && run.Counts.Rows%2 == 0 &&
		confirmed >= 0 && rejected >= 0 && confirmed <= boundedReviewGroups && rejected <= boundedReviewGroups {
		total := confirmed + rejected
		if total != 1 && total <= run.Counts.Rows/2 && run.Revision == run.Counts.Sources+2+total {
			groups, err := checkOriginalBatchReview(tx, run, unsupported)
			return groups, int(total), err
		}
	}
	if run.State != "review" || run.Rules != 1 || run.Closed != nil || run.Deleted != nil || run.Unresolved != nil ||
		run.Counts.Sources != 2 || run.Counts.Rows < 2 || run.Counts.Rows > 2*boundedReviewGroups ||
		confirmed < 0 || rejected < 0 || confirmed > boundedReviewGroups || rejected > boundedReviewGroups {
		return 0, 0, unsupported
	}
	decisions := confirmed + rejected
	if decisions > run.Counts.Rows/2 || run.Revision != 4+decisions {
		return 0, 0, unsupported
	}
	var creator sql.NullInt64
	err := tx.QueryRow(`SELECT r.created_by_id FROM reconciliation_read_runs r
		JOIN reconciliation_native_v1_runs n ON n.run_id=r.id WHERE r.id=$1`, run.ID).Scan(&creator)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !creator.Valid) {
		return 0, 0, unsupported
	}
	if err != nil {
		return 0, 0, err
	}

	// The fixed query count does not depend on G or D. Check cap+1 at
	// each real projection, not only the denormalized run counts.
	sources, err := scanBoundedReview(tx, `SELECT id,row_count,imported_by_id,operation_key,side
		FROM reconciliation_read_sources WHERE run_id=$1 ORDER BY id LIMIT 3`,
		func(rows *sql.Rows) (boundedReviewSource, error) {
			var item boundedReviewSource
			err := rows.Scan(&item.id, &item.rows, &item.actor, &item.key, &item.side)
			return item, err
		}, run.ID)
	if err != nil {
		return 0, 0, err
	}
	if len(sources) != 2 || sources[0].side == sources[1].side || sources[0].key == sources[1].key {
		return 0, 0, unsupported
	}
	sourceByID := make(map[int64]boundedReviewSource, 2)
	for _, source := range sources {
		if source.rows < 1 || source.rows > boundedReviewGroups || !source.actor.Valid || !source.key.Valid || !boundedReviewKey(source.key.String) {
			return 0, 0, unsupported
		}
		sourceByID[source.id] = source
	}
	links, err := scanBoundedReview(tx, `SELECT source_id FROM reconciliation_detail_rows
		WHERE run_id=$1 ORDER BY id LIMIT $2`, func(rows *sql.Rows) (int64, error) {
		var sourceID int64
		err := rows.Scan(&sourceID)
		return sourceID, err
	}, run.ID, 2*boundedReviewGroups+1)
	if err != nil {
		return 0, 0, err
	}
	actual := make(map[int64]int64, 2)
	for _, sourceID := range links {
		if _, ok := sourceByID[sourceID]; !ok {
			return 0, 0, unsupported
		}
		actual[sourceID]++
	}
	if actual[sources[0].id] != sources[0].rows || actual[sources[1].id] != sources[1].rows {
		return 0, 0, unsupported
	}
	normalized, comparables, err := readPrepareRows(tx, run)
	if err != nil {
		return 0, 0, err
	}
	reconstructed, err := buildReconciliationGroups(normalized, run.MatchReference)
	if err != nil || len(reconstructed) == 0 || len(reconstructed) > boundedReviewGroups {
		return 0, 0, unsupported
	}
	// Only the two fixed undecided shapes bypass the matched-pair witness.
	var fixedUndecided bool
	if run.Revision == 4 && decisions == 0 {
		switch {
		case len(normalized) == 2 && twoUnmatchedReviewSingletons(reconstructed):
			if err := checkExactReviewProjection(tx, run.ID, reconstructed, comparables, 2, run.MatchReference, unsupported); err != nil {
				return 0, 0, err
			}
			fixedUndecided = true
		case run.MatchReference && len(normalized) == 5 && len(reconstructed) == 2 &&
			(reconstructed[0].Kind == "ambiguous" || reconstructed[1].Kind == "ambiguous"):
			if err := checkUndecidedAmbiguousReview(tx, run.ID, normalized, reconstructed, comparables, unsupported); err != nil {
				return 0, 0, err
			}
			fixedUndecided = true
		}
	}
	if fixedUndecided {
		if _, err := readBoundedReviewDecisions(tx, run, nil, comparables, unsupported); err != nil {
			return 0, 0, err
		}
		if err := checkBoundedReviewAudit(tx, run, creator.Int64, sourceByID, nil, unsupported); err != nil {
			return 0, 0, err
		}
		return len(reconstructed), 0, nil
	}
	pairs := make(map[int64]boundedReviewPair, len(reconstructed))
	byRow := make(map[int64]reconciliationReferenceRow, len(normalized))
	for _, row := range normalized {
		byRow[row.ID] = row
	}
	// The matcher gives missing references individual group keys. Do not
	// extend this removal path to duplicate singleton rows on the same side.
	seenSingletons := make(map[reconciliationReferenceRow]bool)
	var matched int64
	for i, g := range reconstructed {
		var shape boundedReviewPair
		switch {
		case g.Kind == "candidate" && len(g.Payments) == 1 && len(g.CashFlow) == 1:
			shape = boundedReviewPair{g.Payments[0], g.CashFlow[0]}
			matched++
		case g.Kind == "unmatched" && len(g.Payments)+len(g.CashFlow) == 1:
			if len(g.Payments) == 1 {
				shape.payment = g.Payments[0]
			} else {
				shape.cash = g.CashFlow[0]
			}
			singleton := byRow[shape.payment+shape.cash]
			singleton.ID = 0
			if seenSingletons[singleton] {
				return 0, 0, unsupported
			}
			seenSingletons[singleton] = true
		default:
			return 0, 0, unsupported
		}
		pairs[int64(i+1)] = shape
	}
	if matched == 0 || decisions > matched {
		return 0, 0, unsupported
	}
	if err := checkBoundedReviewGroups(tx, run.ID, pairs, unsupported); err != nil {
		return 0, 0, err
	}
	byDecision, err := readBoundedReviewDecisions(tx, run, pairs, comparables, unsupported)
	if err != nil {
		return 0, 0, err
	}
	if err := checkBoundedReviewAudit(tx, run, creator.Int64, sourceByID, byDecision, unsupported); err != nil {
		return 0, 0, err
	}
	return len(pairs), int(decisions), nil
}

func checkBoundedReviewGroups(tx *sql.Tx, run int64, pairs map[int64]boundedReviewPair, unsupported problem) error {
	type header struct {
		id         int64
		kind       string
		valid      bool
		exactShape bool
	}
	heads, err := scanBoundedReview(tx, `SELECT ordinal,COALESCE((CAST(payload AS json)->>'kind'),''),
		CASE WHEN json_typeof(CAST(payload AS json)->'decisions')='array' THEN json_array_length(CAST(payload AS json)->'decisions')=0 ELSE false END,
		(SELECT COUNT(*)=2 AND COUNT(DISTINCT CASE WHEN key IN ('kind','decisions') THEN key END)=2 FROM json_each(CASE WHEN json_typeof(CAST(payload AS json))='object' THEN CAST(payload AS json) ELSE '{}'::json END))
		FROM reconciliation_detail_groups WHERE run_id=$1 ORDER BY ordinal LIMIT $2`,
		func(rows *sql.Rows) (header, error) {
			var h header
			err := rows.Scan(&h.id, &h.kind, &h.valid, &h.exactShape)
			return h, err
		}, run, boundedReviewGroups+1)
	if err != nil {
		return err
	}
	if len(heads) != len(pairs) {
		return unsupported
	}
	expectedMembers := 0
	for _, h := range heads {
		pair, exists := pairs[h.id]
		if !exists || !h.valid {
			return unsupported
		}
		if pair.matched() {
			if h.kind != "candidate" {
				return unsupported
			}
			expectedMembers += 2
		} else {
			if h.kind != "unmatched" || !h.exactShape {
				return unsupported
			}
			expectedMembers++
		}
	}
	type member struct{ group, row, ordinal int64 }
	members, err := scanBoundedReview(tx, `SELECT group_ordinal,row_id,ordinal FROM reconciliation_detail_members
		WHERE run_id=$1 ORDER BY group_ordinal,row_id LIMIT $2`, func(rows *sql.Rows) (member, error) {
		var m member
		err := rows.Scan(&m.group, &m.row, &m.ordinal)
		return m, err
	}, run, 2*boundedReviewGroups+1)
	if err != nil {
		return err
	}
	if len(members) != expectedMembers {
		return unsupported
	}
	seen := make(map[int64]bool, len(members))
	for _, m := range members {
		pair, exists := pairs[m.group]
		if !exists || m.ordinal != 1 || seen[m.row] || (m.row != pair.payment && m.row != pair.cash) {
			return unsupported
		}
		seen[m.row] = true
	}
	return nil
}

func readBoundedReviewDecisions(tx *sql.Tx, run reconciliationReadRun, pairs map[int64]boundedReviewPair, comparables map[int64]reconciliationComparables, unsupported problem) (map[int64]boundedReviewDecision, error) {
	decisions, err := scanBoundedReview(tx, `SELECT id,group_ordinal,payment_row_id,cash_flow_row_id,actor_id,operation_key,decision,acknowledged
		FROM reconciliation_review_decisions WHERE run_id=$1 ORDER BY group_ordinal,payment_row_id,id LIMIT $2`,
		func(rows *sql.Rows) (boundedReviewDecision, error) {
			var d boundedReviewDecision
			err := rows.Scan(&d.id, &d.group, &d.payment, &d.cash, &d.actor, &d.key, &d.action, &d.acknowledged)
			return d, err
		}, run.ID, boundedReviewGroups+1)
	if err != nil {
		return nil, err
	}
	if int64(len(decisions)) != run.Counts.Confirmed+run.Counts.Rejected {
		return nil, unsupported
	}
	keys, groups := make(map[string]bool, len(decisions)), make(map[int64]bool, len(decisions))
	byID := make(map[int64]boundedReviewDecision, len(decisions))
	var confirmed, rejected int64
	for _, d := range decisions {
		pair, exists := pairs[d.group]
		if !exists || !pair.matched() || d.payment != pair.payment || d.cash != pair.cash ||
			!boundedReviewKey(d.key) || keys[d.key] || groups[d.group] {
			return nil, unsupported
		}
		switch d.action {
		case "confirmed":
			if !d.acknowledged && hasReconciliationDiscrepancy(comparables[d.payment], comparables[d.cash]) {
				return nil, unsupported
			}
			confirmed++
		case "rejected":
			rejected++
		default:
			return nil, unsupported
		}
		keys[d.key], groups[d.group], byID[d.id] = true, true, d
	}
	if confirmed != run.Counts.Confirmed || rejected != run.Counts.Rejected {
		return nil, unsupported
	}
	return byID, nil
}

func checkBoundedReviewAudit(tx *sql.Tx, run reconciliationReadRun, creator int64, sources map[int64]boundedReviewSource, decisions map[int64]boundedReviewDecision, unsupported problem) error {
	events, err := readTypedReviewAudit(tx, run.ID, 4+boundedReviewGroups+1)
	if err != nil {
		return err
	}
	if len(events) != 4+len(decisions) {
		return unsupported
	}
	var seenSource, lastDecision int64
	for i, entry := range events {
		event := entry.event
		if !entry.strict || !entry.actor.Valid || event.Actor == nil || entry.ordinal != int64(i+1) ||
			event.Revision != entry.ordinal {
			return unsupported
		}
		time := event.Occurred
		if _, err := reconciliationWireTime(&time); err != nil {
			return unsupported
		}
		if i <= 3 && (event.Payment != nil || event.Cash != nil || event.Match != nil) {
			return unsupported
		}
		switch i {
		case 0:
			if entry.actor.Int64 != creator || entry.key.Valid || event.Action != "create" ||
				event.Source != nil || event.Occurred != run.Created {
				return unsupported
			}
		case 1, 2:
			if entry.key.Valid || event.Action != "import" || event.Source == nil || seenSource == *event.Source {
				return unsupported
			}
			source, exists := sources[*event.Source]
			if !exists || source.actor.Int64 != entry.actor.Int64 {
				return unsupported
			}
			seenSource = *event.Source
		case 3:
			if !entry.key.Valid || !boundedReviewKey(entry.key.String) || event.Action != "prepare" || event.Source != nil {
				return unsupported
			}
		default:
			if entry.key.Valid || event.Source != nil || event.Payment == nil || event.Cash == nil ||
				event.Match == nil || *event.Match <= lastDecision {
				return unsupported
			}
			d, exists := decisions[*event.Match]
			if !exists || d.actor != entry.actor.Int64 || d.payment != *event.Payment ||
				d.cash != *event.Cash || d.action != event.Action {
				return unsupported
			}
			lastDecision = *event.Match
		}
	}
	return nil
}

func readTypedReviewAudit(tx *sql.Tx, run int64, limit int) ([]boundedReviewAudit, error) {
	return scanBoundedReview(tx, `SELECT ordinal,actor_id,operation_key,payload,
		(SELECT COUNT(*)=8 AND COUNT(DISTINCT CASE WHEN key IN (
			'action','actor','occurred_at','run_revision','source_id','payment_row_id','cash_flow_row_id','match_id'
		) THEN key END)=8 FROM json_each(CASE WHEN json_typeof(CAST(payload AS json))='object' THEN CAST(payload AS json) ELSE '{}'::json END))
		FROM reconciliation_detail_events WHERE run_id=$1 ORDER BY ordinal LIMIT $2`,
		func(rows *sql.Rows) (boundedReviewAudit, error) {
			var e boundedReviewAudit
			var raw string
			err := rows.Scan(&e.ordinal, &e.actor, &e.key, &raw, &e.strict)
			if err == nil && json.Unmarshal([]byte(raw), &e.event) != nil {
				e.strict = false
			}
			return e, err
		}, run, limit)
}
