package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"
)

const (
	intakeEntityBytes   = 100 << 20
	intakeEntityFiles   = 32
	intakeServiceBytes  = 1 << 30
	intakeServiceFiles  = 512
	intakeSweepBatch    = 16
	intakeSweepInterval = time.Minute
	intakeSweepTimeout  = 2 * time.Second
)

var intakeStorageBudget = problem{409, "Se alcanzó el límite de cargas temporales. Descartá las que no uses o intentá más tarde."}

// Uploads hold this lock until the referenced file is durable and committed.
// Cleanup releases capacity only after durable unlink and reference removal.
func requireIntakeCapacity(ctx context.Context, tx *sql.Tx, entityID, size int64) error {
	if _, err := tx.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_xact_lock(1196380498, 2)"); err != nil {
		return err
	}
	var entityFiles, entityBytes, files, bytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE entity_id=$1),
		COALESCE(SUM(size) FILTER (WHERE entity_id=$1),0),COUNT(*),COALESCE(SUM(size),0)
		FROM intake_operations WHERE storage_name IS NOT NULL`, entityID).
		Scan(&entityFiles, &entityBytes, &files, &bytes); err != nil {
		return err
	}
	if entityFiles >= intakeEntityFiles || entityBytes+size > intakeEntityBytes ||
		files >= intakeServiceFiles || bytes+size > intakeServiceBytes {
		return intakeStorageBudget
	}
	return nil
}

func (a *app) stagedIntakeSweepLoop(ctx context.Context) {
	ticker := time.NewTicker(intakeSweepInterval)
	defer ticker.Stop()
	cursor := ""
	for {
		next, err := a.sweepStagedIntake(ctx, cursor, time.Now().UnixMicro())
		cursor = next
		if err != nil && ctx.Err() == nil {
			log.Print("could not finish staged intake maintenance; will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Advance past failed rows so a damaged reference cannot starve other cleanup.
func (a *app) sweepStagedIntake(ctx context.Context, after string, now int64) (string, error) {
	readCtx, cancel := context.WithTimeout(ctx, intakeSweepTimeout)
	defer cancel()
	rows, err := a.db.QueryContext(readCtx, `SELECT id,entity_id,status FROM intake_operations
		WHERE storage_name IS NOT NULL AND id>$1
		AND (status IN ('cancelled','expired','confirmed') OR expires_at<=$2)
		ORDER BY id LIMIT $3`, after, now, intakeSweepBatch)
	if err != nil {
		return after, err
	}
	defer rows.Close()
	var candidates []intakeRecord
	for rows.Next() {
		var op intakeRecord
		if err := rows.Scan(&op.id, &op.entityID, &op.status); err != nil {
			return after, err
		}
		candidates = append(candidates, op)
	}
	if err := rows.Err(); err != nil {
		return after, err
	}
	rows.Close()

	var sweepErr error
	for _, op := range candidates {
		if err := ctx.Err(); err != nil {
			return after, err
		}
		itemCtx, stop := context.WithTimeout(ctx, intakeSweepTimeout)
		var err error
		if op.live() {
			err = a.expireStagedIntake(itemCtx, op, now)
		}
		if err == nil {
			err = a.cleanupTerminalIntake(itemCtx, op.id)
		}
		stop()
		sweepErr = errors.Join(sweepErr, err)
		after = op.id
	}
	if len(candidates) < intakeSweepBatch {
		after = ""
	}
	return after, sweepErr
}

func (a *app) expireStagedIntake(ctx context.Context, op intakeRecord, now int64) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Use the same entity lock as interactive intake operations. Recheck the
	// current status and deadline so a committed confirmation always wins.
	if err := lockPostgresEntity(ctx, tx, op.entityID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE intake_operations SET status='expired'
		WHERE id=$1 AND entity_id=$2 AND expires_at<=$3
		AND status IN ('uploaded','processing','ready','failed')`, op.id, op.entityID, now); err != nil {
		return err
	}
	return tx.Commit()
}
