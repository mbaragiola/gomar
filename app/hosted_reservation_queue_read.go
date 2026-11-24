package main

import (
	"database/sql"
	"net/http"
)

// Only the named staff queue GET uses this path in the isolated
// composition. Its existing protected principal/employee policy runs
// before the transaction, as on the unconfigured app.
func (a *app) hostedNativeReservationQueueRead() http.HandlerFunc {
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
		m, err := scopedMembership(tx, u.id, entity, "")
		if err != nil {
			return err
		}
		outlet, err := pathID(r, "outlet")
		if err != nil {
			return err
		}
		if err := requireReservationStaff(r.Context(), tx, m, outlet); err != nil {
			return err
		}
		if err := a.admitReservationQueueRead(r, tx, m, outlet); err != nil {
			return err
		}
		code, projection, err := listReservationRequests(r, tx, outlet, nil)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, code, projection)
		return nil
	})
}

// Called only by queue list/detail and day status, inside their existing
// repeatable-read transaction. Do not attach this to the shared scope for
// availability, proposals or any reservation operation.
func (a *app) mcpReservationReadOutlet(r *http.Request, tx *sql.Tx, s mcpTokenScope) (int64, error) {
	outlet, err := mcpReservationQueueScope(r, tx, s)
	if err != nil {
		return 0, err
	}
	return outlet, a.admitReservationQueueRead(r, tx, s.entityMembership, outlet)
}

const hostedReservationQueueReadSQL = `
WITH discovery_input(actor_id,entity_id,outlet_id,checked_at,native_entity_list) AS (
    VALUES ($1::bigint,$2::bigint,$3::bigint,COALESCE($4::timestamptz,statement_timestamp()),FALSE)
), ` + hostedScopeEligibilitySQL + `
SELECT EXISTS(SELECT 1 FROM discovery_admitted)`

func (a *app) admitReservationQueueRead(r *http.Request, tx *sql.Tx, m entityMembership, outlet int64) error {
	f := a.hostedDiscoveryRead
	if f == nil {
		return nil
	}
	var checkedAt sql.NullTime
	if f.now != nil {
		checkedAt = sql.NullTime{Time: f.clock(), Valid: true}
	}
	var admitted bool
	if err := tx.QueryRowContext(r.Context(), hostedReservationQueueReadSQL, m.userID, m.ID, outlet, checkedAt).Scan(&admitted); err != nil {
		return err
	}
	if !admitted {
		return missingOutlet
	}
	return nil
}
