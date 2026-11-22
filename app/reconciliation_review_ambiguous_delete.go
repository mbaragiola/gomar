package main

import (
	"database/sql"
	"strings"
)

// Witness exactly one original 2+1 ambiguous reference and one different
// 1+1 reference, with no hidden/moved rows or derived decision payload.
func checkUndecidedAmbiguousReview(tx *sql.Tx, run int64, normalized []reconciliationReferenceRow,
	groups []reconciliationReferenceGroup, comparables map[int64]reconciliationComparables, unsupported problem) error {
	refs := make(map[int64]string, 5)
	for _, row := range normalized {
		if strings.TrimSpace(row.Reference) == "" {
			return unsupported
		}
		refs[row.ID] = row.Reference
	}
	references := make(map[string]bool, 2)
	for _, group := range groups {
		p, c := len(group.Payments), len(group.CashFlow)
		if !(group.Kind == "candidate" && p == 1 && c == 1 ||
			group.Kind == "ambiguous" && (p == 1 && c == 2 || p == 2 && c == 1)) {
			return unsupported
		}
		ref := refs[group.Payments[0]]
		if ref == "" || references[ref] {
			return unsupported
		}
		references[ref] = true
		for _, side := range [][]int64{group.Payments, group.CashFlow} {
			for _, id := range side {
				if refs[id] != ref {
					return unsupported
				}
			}
		}
	}
	return checkExactReviewProjection(tx, run, groups, comparables, 5, true, unsupported)
}

// Shared by bounded, already reconstructed removal shapes. The
// native Prepare writer is authoritative for header bytes and side order.
func checkExactReviewProjection(tx *sql.Tx, run int64, groups []reconciliationReferenceGroup,
	comparables map[int64]reconciliationComparables, rowCount int, matchReference bool, unsupported problem) error {
	type projectedMember struct{ group, ordinal int64 }
	wanted := make(map[int64]projectedMember, rowCount)
	for i, group := range groups {
		for _, side := range [][]int64{group.Payments, group.CashFlow} {
			for j, id := range side {
				if _, duplicate := wanted[id]; duplicate {
					return unsupported
				}
				wanted[id] = projectedMember{int64(i + 1), int64(j + 1)}
			}
		}
	}

	// The plus-one cap checks the real tables rather than just their counters.
	type header struct {
		ordinal int64
		raw     string
	}
	headers, err := scanBoundedReview(tx, `SELECT ordinal,payload FROM reconciliation_detail_groups
		WHERE run_id=$1 ORDER BY ordinal LIMIT $2`, func(rows *sql.Rows) (header, error) {
		var h header
		err := rows.Scan(&h.ordinal, &h.raw)
		return h, err
	}, run, len(groups)+1)
	if err != nil {
		return err
	}
	if len(headers) != len(groups) {
		return unsupported
	}
	for i, h := range headers {
		canonical, err := prepareGroupPayload(groups[i], comparables)
		if err != nil {
			return err
		}
		if h.ordinal != int64(i+1) || h.raw != string(canonical) {
			return unsupported
		}
	}

	type member struct {
		row int64
		projectedMember
		scoped bool
	}
	members, err := scanBoundedReview(tx, `SELECT m.row_id,m.group_ordinal,m.ordinal,
		EXISTS(SELECT 1 FROM reconciliation_detail_rows r
			JOIN reconciliation_read_sources s ON s.run_id=r.run_id AND s.id=r.source_id
			JOIN reconciliation_read_outlets o ON o.run_id=r.run_id AND o.outlet_id=r.outlet_id
			WHERE r.run_id=m.run_id AND r.id=m.row_id AND (NOT $1 OR (json_typeof(CAST(s.mapping AS json)->'reference')='number' AND (CAST(s.mapping AS json)->>'reference') ~ '^-?[0-9]+$'))
			AND (CAST(r.payload AS jsonb)->'id')=to_jsonb(r.id) AND (CAST(r.payload AS jsonb)->'outlet_id')=to_jsonb(r.outlet_id)
			AND (CAST(r.payload AS jsonb)->'record_number')=to_jsonb(r.record_number)
			AND (CAST(r.payload AS json)->>'source')=s.label AND (CAST(r.payload AS json)->>'side')=s.side
			AND (SELECT COUNT(*)=15 AND COUNT(DISTINCT CASE
				WHEN key IN ('id','outlet_id','record_number') AND type='integer' THEN key
				WHEN key IN ('date','amount','currency','currency_source','source','side') AND type='text' THEN key
				WHEN key IN ('category','description','payment_method','vat_rate','vat_amount','reference') AND type IN ('text','null') THEN key
			END)=15 FROM json_each(CASE WHEN json_typeof(CAST(r.payload AS json))='object' THEN CAST(r.payload AS json) ELSE '{}'::json END)))
		FROM reconciliation_detail_members m WHERE m.run_id=$2 ORDER BY m.group_ordinal,m.row_id LIMIT $3`,
		func(rows *sql.Rows) (member, error) {
			var m member
			err := rows.Scan(&m.row, &m.group, &m.ordinal, &m.scoped)
			return m, err
		}, matchReference, run, rowCount+1)
	if err != nil {
		return err
	}
	if len(members) != len(wanted) || len(wanted) != rowCount {
		return unsupported
	}
	for _, m := range members {
		expected, exists := wanted[m.row]
		if !exists || !m.scoped || expected != m.projectedMember {
			return unsupported
		}
		delete(wanted, m.row)
	}
	return nil
}
