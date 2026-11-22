package main

import (
	"database/sql"
	"errors"
)

// Serialize writes and operation replay to a single visible run. Check
// current membership and grants before locking; read the run state only
// after acquiring this lock to get a fresh READ COMMITTED snapshot.
func lockReconciliationRun(tx *sql.Tx, u user, entity, id int64, includeDeleted bool) error {
	member, err := scopedMembership(tx, u.id, entity, "reconcile_financial_data")
	if err != nil {
		return err
	}
	var found int64
	err = tx.QueryRow(`SELECT r.id FROM reconciliation_read_runs r
		WHERE r.entity_id=$1 AND r.id=$2 AND ($3 OR r.state<>'deleted')
		AND EXISTS(SELECT 1 FROM reconciliation_read_outlets ro WHERE ro.run_id=r.id)
		AND NOT EXISTS(SELECT 1 FROM reconciliation_read_outlets ro WHERE ro.run_id=r.id AND NOT EXISTS(
			SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
			WHERE g.membership_id=$4 AND o.id=ro.outlet_id AND o.entity_id=r.entity_id AND o.active=TRUE))
		FOR UPDATE OF r`, member.ID, id, includeDeleted, member.id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return problem{404, "No se encontró la conciliación."}
	}
	return err
}
