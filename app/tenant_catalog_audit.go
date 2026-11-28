package main

import (
	"context"
	"database/sql"
	"encoding/json"
)

type tenantLegalSnapshot struct {
	Name string `json:"name"`
	CUIT string `json:"cuit"`
}

type tenantOutletSnapshot struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

func recordTenantCatalogEvent(ctx context.Context, tx *sql.Tx, entityID, actorID int64, outletID sql.NullInt64, action string, before, after any) error {
	// Snapshots contain only strings and booleans.
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	_, err := tx.ExecContext(ctx, `INSERT INTO tenant_catalog_events
		(entity_id,outlet_id,actor_id,action,before,after) VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb)`,
		entityID, outletID, actorID, action, string(beforeJSON), string(afterJSON))
	return err
}
