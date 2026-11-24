package main

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
)

// Authorize POS and stock writes against current scope, including exact
// replays. Use the same order as scoped access management and MCP approvals:
// actor (when recorded), entity, membership, outlet, then grant.
func lockPOSWriteScope(capability string, recordsActor bool, next scopedHandler) scopedHandler {
	allowed := func(role string) bool {
		return posSellingRole(role) && slices.Contains(capabilities[role], capability)
	}
	return func(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
		if !allowed(m.role) {
			return 0, nil, forbiddenRole
		}
		ctx := r.Context()
		var id int64
		if recordsActor {
			// Take the FK lock before domain locks. Browser approvals and
			// employee writes can already hold UPDATE on this actor.
			err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND active FOR KEY SHARE`, m.userID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil, unauthenticated
			}
			if err != nil {
				return 0, nil, err
			}
		}
		// Access writers use FOR UPDATE. Share the lock between writes to
		// different outlets; an administrative route may already hold it.
		err := tx.QueryRowContext(ctx, `SELECT id FROM entities WHERE id=$1 AND active FOR SHARE`, m.ID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, missingEntity
		}
		if err != nil {
			return 0, nil, err
		}
		err = tx.QueryRowContext(ctx, `SELECT role FROM memberships
			WHERE id=$1 AND user_id=$2 AND entity_id=$3 AND active FOR SHARE`,
			m.id, m.userID, m.ID).Scan(&m.role)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, missingEntity
		}
		if err != nil {
			return 0, nil, err
		}
		if !allowed(m.role) {
			return 0, nil, forbiddenRole
		}
		outlet, err := posOutlet(r, tx, m)
		if err != nil {
			return 0, nil, err
		}
		if err := lockPostgresOutlet(ctx, tx, outlet); errors.Is(err, sql.ErrNoRows) {
			return 0, nil, missingOutlet
		} else if err != nil {
			return 0, nil, err
		}
		// Recheck the selected outlet and freeze the exact grant after any
		// outlet wait, before the handler reads or writes business data.
		err = tx.QueryRowContext(ctx, `SELECT g.outlet_id FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
			WHERE g.membership_id=$1 AND g.outlet_id=$2 AND o.entity_id=$3 AND o.active FOR SHARE OF g`,
			m.id, outlet, m.ID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, missingOutlet
		}
		if err != nil {
			return 0, nil, err
		}
		return next(r, tx, m, raw)
	}
}
