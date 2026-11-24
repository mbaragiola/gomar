package main

import (
	"database/sql"
	"net/http"
)

// In the isolated composition only this delivery GET changes its
// transaction mode. The standard login and employee/org policy stays
// outside the scoped transaction, as on the unconfigured app.
func (a *app) hostedNativeDeliveryRead() http.HandlerFunc {
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
		code, rows, err := a.listDeliveryOrders(r, tx, m, nil)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, code, rows)
		return nil
	})
}

func (a *app) mcpDeliveryReadOutlet(r *http.Request, tx *sql.Tx, s mcpTokenScope) (int64, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return 0, err
	}
	return outlet, a.admitDeliveryRead(r, tx, s.entityMembership, outlet)
}

const hostedDeliveryReadSQL = `
WITH discovery_input(actor_id,entity_id,outlet_id,checked_at,native_entity_list) AS (
    VALUES ($1::bigint,$2::bigint,$3::bigint,COALESCE($4::timestamptz,statement_timestamp()),FALSE)
), ` + hostedScopeEligibilitySQL + `
SELECT EXISTS(SELECT 1 FROM discovery_admitted)`

// Both delivery projections and the current exact outlet are read in the
// caller's original protected read-only snapshot. No ingestion is involved.
func (a *app) admitDeliveryRead(r *http.Request, tx *sql.Tx, m entityMembership, outlet int64) error {
	f := a.hostedDiscoveryRead
	if f == nil {
		return nil
	}
	var checkedAt sql.NullTime
	if f.now != nil {
		checkedAt = sql.NullTime{Time: f.clock(), Valid: true}
	}
	var admitted bool
	if err := tx.QueryRowContext(r.Context(), hostedDeliveryReadSQL, m.userID, m.ID, outlet, checkedAt).Scan(&admitted); err != nil {
		return err
	}
	if !admitted {
		return missingOutlet
	}
	return nil
}
