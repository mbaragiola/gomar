package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"time"
)

type reconciliationReadCounts struct {
	Sources   int64 `json:"sources"`
	Rows      int64 `json:"rows"`
	Confirmed int64 `json:"confirmed"`
	Rejected  int64 `json:"rejected"`
}

type reconciliationReadRun struct {
	ID             int64                    `json:"id"`
	EntityID       int64                    `json:"entity_id"`
	Outlets        []int64                  `json:"outlet_ids"`
	State          string                   `json:"state"`
	Revision       int64                    `json:"revision"`
	Rules          int64                    `json:"rules_version"`
	MatchReference bool                     `json:"match_reference"`
	Created        string                   `json:"created_at"`
	Closed         *string                  `json:"closed_at"`
	Deleted        *string                  `json:"deleted_at"`
	Unresolved     *int64                   `json:"unresolved_count"`
	Counts         reconciliationReadCounts `json:"counts"`
	createdInstant time.Time
}

type reconciliationReadSource struct {
	ID       int64           `json:"id"`
	Side     string          `json:"side"`
	Label    string          `json:"label"`
	Mapping  json.RawMessage `json:"mapping"`
	Formats  json.RawMessage `json:"formats"`
	Currency *string         `json:"default_currency"`
	Outlet   *int64          `json:"default_outlet_id"`
	Rows     int64           `json:"row_count"`
}

func reconciliationWireTime(value *string) (time.Time, error) {
	if value == nil {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return t, err
	}
	layout := time.RFC3339
	if t.Nanosecond() != 0 {
		layout = "2006-01-02T15:04:05.000Z07:00"
	}
	*value = t.UTC().Format(layout)
	return t, nil
}

func scanReconciliationRuns(tx *sql.Tx, m entityMembership, id any) ([]reconciliationReadRun, error) {
	return scanReconciliationRunsIncludingState(tx, m, id, false)
}

func scanReconciliationRunsIncludingState(tx *sql.Tx, m entityMembership, id any, includeDeleted bool) ([]reconciliationReadRun, error) {
	rows, err := tx.Query(`SELECT r.id,r.entity_id,r.state,r.revision,r.rules_version,r.match_reference,
		r.created_at,r.closed_at,r.deleted_at,r.unresolved_count,r.confirmed_count,r.rejected_count,
		(SELECT COUNT(*) FROM reconciliation_read_sources s WHERE s.run_id=r.id),
		(SELECT COALESCE(SUM(s.row_count),0) FROM reconciliation_read_sources s WHERE s.run_id=r.id)
		FROM reconciliation_read_runs r
		WHERE r.entity_id=$1 AND ($2 OR r.state<>'deleted') AND (CAST($3 AS BIGINT) IS NULL OR r.id=$4)
		AND EXISTS(SELECT 1 FROM reconciliation_read_outlets ro WHERE ro.run_id=r.id)
		AND NOT EXISTS (
			SELECT 1 FROM reconciliation_read_outlets ro WHERE ro.run_id=r.id AND NOT EXISTS (
				SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
				WHERE g.membership_id=$5 AND o.id=ro.outlet_id AND o.entity_id=r.entity_id AND o.active=TRUE
			)
		)`, m.ID, includeDeleted, id, id, m.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []reconciliationReadRun{}
	for rows.Next() {
		var run reconciliationReadRun
		err := rows.Scan(&run.ID, &run.EntityID, &run.State, &run.Revision, &run.Rules, &run.MatchReference,
			&run.Created, &run.Closed, &run.Deleted, &run.Unresolved, &run.Counts.Confirmed, &run.Counts.Rejected,
			&run.Counts.Sources, &run.Counts.Rows)
		if err != nil {
			return nil, err
		}
		run.createdInstant, err = reconciliationWireTime(&run.Created)
		if err != nil {
			return nil, err
		}
		for _, when := range []*string{run.Closed, run.Deleted} {
			if _, err := reconciliationWireTime(when); err != nil {
				return nil, err
			}
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(result, func(a, b reconciliationReadRun) int {
		if compare := b.createdInstant.Compare(a.createdInstant); compare != 0 {
			return compare
		}
		if a.ID > b.ID {
			return -1
		}
		if a.ID < b.ID {
			return 1
		}
		return 0
	})
	return result, nil
}

func reconciliationOutlets(tx *sql.Tx, id int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT o.id FROM reconciliation_read_outlets ro JOIN outlets o ON o.id=ro.outlet_id
		WHERE ro.run_id=$1 ORDER BY o.name,o.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func reconciliationSources(tx *sql.Tx, id int64) ([]reconciliationReadSource, error) {
	rows, err := tx.Query(`SELECT id,side,label,mapping,formats,NULLIF(default_currency,''),default_outlet_id,row_count
		FROM reconciliation_read_sources WHERE run_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []reconciliationReadSource{}
	for rows.Next() {
		var source reconciliationReadSource
		var mapping, formats string
		if err := rows.Scan(&source.ID, &source.Side, &source.Label, &mapping, &formats, &source.Currency, &source.Outlet, &source.Rows); err != nil {
			return nil, err
		}
		source.Mapping, source.Formats = json.RawMessage(mapping), json.RawMessage(formats)
		result = append(result, source)
	}
	return result, rows.Err()
}

func (a *app) readReconciliation(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	var id any
	if r.PathValue("run") != "" {
		id, err = pathID(r, "run")
		if err != nil {
			return err
		}
	}
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entity, "reconcile_financial_data")
	if err != nil {
		return err
	}
	runs, err := scanReconciliationRuns(tx, member, id)
	if err != nil {
		return err
	}
	for i := range runs {
		runs[i].Outlets, err = reconciliationOutlets(tx, runs[i].ID)
		if err != nil {
			return err
		}
	}
	var body any = runs
	if id != nil {
		if len(runs) == 0 {
			return problem{404, "No se encontró la conciliación."}
		}
		sources, err := reconciliationSources(tx, runs[0].ID)
		if err != nil {
			return err
		}
		body = struct {
			reconciliationReadRun
			Sources []reconciliationReadSource `json:"sources"`
		}{runs[0], sources}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, body)
	return nil
}
