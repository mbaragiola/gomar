package main

import (
	"database/sql"
	"net/http"
)

// Existing stock checks and data queries share one repeatable-read snapshot.
// Only these native GETs change transaction mode, and only in the explicitly
// injected hosted fixture. The normal browser principal and employee/org
// wrapper still run before the read transaction, as on the default path.
func (a *app) hostedNativeStockRead(next scopedHandler) http.HandlerFunc {
	return a.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
		entity, err := pathID(r, "entity")
		if err != nil {
			return err
		}
		tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		m, err := scopedMembership(tx, u.id, entity, "read_business_data")
		if err != nil {
			return err
		}
		if !stockReadRole(m.role) {
			return forbiddenRole
		}
		m.employee = u.employee
		status, result, err := next(r, tx, m, nil)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, status, result)
		return nil
	})
}

func (a *app) nativeStockReadOutlet(r *http.Request, tx *sql.Tx, m entityMembership) (int64, error) {
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, err
	}
	return outlet, a.admitStockRead(r, tx, m, outlet)
}

func (a *app) mcpStockReadOutlet(r *http.Request, tx *sql.Tx, s mcpTokenScope) (int64, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return 0, err
	}
	return outlet, a.admitStockRead(r, tx, s.entityMembership, outlet)
}

const hostedStockReadSQL = `
WITH discovery_input(actor_id,entity_id,outlet_id,checked_at,native_entity_list) AS (
    VALUES ($1::bigint,$2::bigint,$3::bigint,COALESCE($4::timestamptz,statement_timestamp()),FALSE)
), ` + hostedScopeEligibilitySQL + `
SELECT EXISTS(SELECT 1 FROM discovery_admitted)`

// The caller has already enforced stock role, active outlet grant and (for
// MCP) token scope. This additional exact-outlet check exposes no scope or
// product metadata and runs inside the very same transaction as the reader.
func (a *app) admitStockRead(r *http.Request, tx *sql.Tx, m entityMembership, outlet int64) error {
	f := a.hostedDiscoveryRead
	if f == nil {
		return nil
	}
	var checkedAt sql.NullTime
	if f.now != nil {
		checkedAt = sql.NullTime{Time: f.clock(), Valid: true}
	}
	var admitted bool
	if err := tx.QueryRowContext(r.Context(), hostedStockReadSQL, m.userID, m.ID, outlet, checkedAt).Scan(&admitted); err != nil {
		return err
	}
	if !admitted {
		return missingOutlet
	}
	return nil
}
