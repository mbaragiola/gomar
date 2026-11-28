package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

const initialOutletGrantee = ` FROM memberships m JOIN users u ON u.id=m.user_id JOIN entities e ON e.id=m.entity_id
	WHERE e.active AND m.active AND m.role='administration' AND u.active AND NOT u.staff AND NOT u.superuser
	AND NOT EXISTS(SELECT 1 FROM organization_employees oe WHERE oe.user_id=u.id)`

var invalidInitialOutletGrantee = problem{403, "El permiso inicial requiere una membresía activa de administración de esta entidad."}

// Both callers hold the entity lock and pass only the outlet they just inserted.
func grantNewOutlet(ctx context.Context, tx *sql.Tx, entityID, outletID, membershipID, actorID int64) error {
	var before accessSnapshot
	err := tx.QueryRowContext(ctx, "SELECT m.role,m.active"+initialOutletGrantee+
		" AND e.id=$1 AND m.id=$2 FOR SHARE OF m,u", entityID, membershipID).Scan(&before.Role, &before.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return invalidInitialOutletGrantee
	}
	if err != nil {
		return err
	}
	outlets, valid, err := grantScope(tx, membershipID, entityID)
	if err != nil {
		return err
	}
	if !valid {
		return invalidInitialOutletGrantee
	}
	before.Outlets = outlets
	slices.Sort(before.Outlets)
	if _, err := tx.ExecContext(ctx, "INSERT INTO outlet_grants(membership_id,outlet_id) VALUES($1,$2)", membershipID, outletID); err != nil {
		return err
	}
	after := before
	after.Outlets = append(slices.Clone(before.Outlets), outletID)
	slices.Sort(after.Outlets)
	return recordAccessEvent(ctx, tx, entityID, membershipID, actorID, "update_membership", before, after)
}

func recordAccessEvent(ctx context.Context, tx *sql.Tx, entityID, membershipID, actorID int64, action string, before any, after accessSnapshot) error {
	// The snapshots only contain strings, booleans and integer IDs.
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	_, err := tx.ExecContext(ctx, `INSERT INTO access_events(entity_id,membership_id,actor_id,action,before,after,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, entityID, membershipID, actorID, action,
		string(beforeJSON), string(afterJSON), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
