package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed postgres_migrations/*.sql
var postgresMigrations embed.FS

// The hosted schema has its own migration history. Do not assume that its
// version numbers match the SQLite user_version used on the local appliance.
var postgresMigrationNames = []string{
	"001_hosted_core.sql",
	"002_hosted_operations.sql",
	"003_full_application.sql",
	"004_full_invariants.sql",
	"005_mcp_read_access.sql",
	"006_mcp_wiki_proposals.sql",
	"007_mcp_document_proposals.sql",
	"008_mcp_wiki_edit_proposals.sql",
	"009_mcp_stock_receipt_proposals.sql",
	"010_mcp_proposal_sessions.sql",
	"011_mcp_document_review_proposals.sql",
	"012_mcp_reconciliation_draft_proposals.sql",
	"013_mcp_document_edit_proposals.sql",
	"014_mcp_stock_adjustment_proposals.sql",
	"015_mcp_pos_catalog_proposals.sql",
	"016_mcp_reservation_confirm_proposals.sql",
	"017_mcp_reservation_cancel_proposals.sql",
	"018_provider_auth.sql",
	"019_admin_recovery.sql",
	"020_mcp_pos_create_proposals.sql",
	"021_mcp_stock_minimum_proposals.sql",
	"022_supplier_legal_name_proposals.sql",
	"023_reservation_customers.sql",
	"024_mcp_stock_tracking_proposals.sql",
	"025_mcp_stock_product_setup_proposals.sql",
	"026_mcp_reservation_table_add_proposals.sql",
	"027_mcp_reservation_slot_add_proposals.sql",
	"028_mcp_outlet_display_label_proposals.sql",
	"029_mcp_document_number_proposals.sql",
	"030_mcp_reservation_policy_notice_proposals.sql",
	"031_mcp_document_point_of_sale_proposals.sql",
	"032_employee_onboarding.sql",
	"033_pos_product_barcode.sql",
	"034_pos_table_checks.sql",
	"035_pos_table_check_retirement.sql",
	"036_pos_manual_tender.sql",
	"037_pos_table_finalization.sql",
	"038_pos_table_check_save_rejections.sql",
	"039_pos_sale_till_time.sql",
	"040_native_owner_consent.sql",
	"041_native_owner_dormant_install.sql",
	"042_native_owner_display_binding.sql",
}

func postgresConfig(dsn string) (*pgx.ConnConfig, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PostgreSQL connection settings are missing")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg == nil || cfg.Host == "" || cfg.Database == "" || cfg.User == "" {
		return nil, errors.New("invalid PostgreSQL connection settings")
	}
	if len(cfg.Fallbacks) != 0 {
		return nil, errors.New("PostgreSQL requires a single endpoint without connection fallbacks")
	}
	if !filepath.IsAbs(cfg.Host) && (cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName == "") {
		return nil, errors.New("remote PostgreSQL connections require certificate and hostname verification")
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = make(map[string]string)
	}
	cfg.RuntimeParams["search_path"] = "pg_catalog, gomar"
	cfg.RuntimeParams["timezone"] = "UTC"
	if cfg.ConnectTimeout <= 0 || cfg.ConnectTimeout > 5*time.Second {
		cfg.ConnectTimeout = 5 * time.Second
	}
	return cfg, nil
}

func connectPostgres(ctx context.Context, dsn string) (*sql.DB, error) {
	cfg, err := postgresConfig(dsn)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		// pgx connection errors can include details from configuration.
		return nil, errors.New("could not connect to PostgreSQL")
	}
	return db, nil
}

func preparePostgresState(state string) error {
	if err := privateDir(state); err != nil {
		return err
	}
	if err := rejectSQLiteState(state); err != nil {
		return err
	}
	return privateDir(filepath.Join(state, "private"))
}

func verifyPostgresState(state string) error {
	for _, path := range []string{state, filepath.Join(state, "private")} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return errors.New("state and private storage must be directories with mode 0700")
		}
	}
	return rejectSQLiteState(state)
}

// Refuse to start in a legacy state directory. This never reads, changes,
// upgrades, or deletes any old database or backup.
func rejectSQLiteState(state string) error {
	for _, name := range []string{"margot.sqlite3", "margot.sqlite3-wal", "margot.sqlite3-shm"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return errors.New("PostgreSQL requires a state directory separate from SQLite")
		}
	}
	return nil
}

// All hosted writes that compare an outlet's existing orders, occupancy or
// stock take this lock first. SQLite already serializes writers when their
// transactions begin; it must never call the PostgreSQL-only statement.
func lockPostgresOutlet(ctx context.Context, tx *sql.Tx, outlet int64) error {
	var id int64
	return tx.QueryRowContext(ctx, `SELECT id FROM gomar.outlets WHERE id=$1 FOR UPDATE`, outlet).Scan(&id)
}

// Accounting mutations can cross outlets within an entity. Take this lock
// before any narrower outlet or document lock for those operations.
func lockPostgresEntity(ctx context.Context, tx *sql.Tx, entity int64) error {
	var id int64
	return tx.QueryRowContext(ctx, `SELECT id FROM gomar.entities WHERE id=$1 FOR UPDATE`, entity).Scan(&id)
}

func verifyPostgresMigrations(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT version,name,digest FROM gomar.schema_migrations ORDER BY version`)
	if err != nil {
		return errors.New("PostgreSQL schema is not ready; initialize it before starting Gomar")
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var version int
		var name, digest string
		if err := rows.Scan(&version, &name, &digest); err != nil {
			return err
		}
		if version != seen+1 || version > len(postgresMigrationNames) || name != postgresMigrationNames[seen] {
			return errors.New("unsupported PostgreSQL schema version")
		}
		script, err := postgresMigrations.ReadFile("postgres_migrations/" + name)
		if err != nil {
			return err
		}
		if digest != fmt.Sprintf("%x", sha256.Sum256(script)) {
			return errors.New("PostgreSQL schema migration checksum mismatch")
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if seen != len(postgresMigrationNames) {
		return errors.New("PostgreSQL schema requires migration")
	}
	return nil
}

// migratePostgres is an explicit administration step. The app's regular
// connection need not own the schema or have permission to change it.
func migratePostgres(ctx context.Context, db *sql.DB) error {
	return migratePostgresThrough(ctx, db, postgresMigrationNames)
}

// Keep the same validation and transaction path for upgrade rehearsals.
func migratePostgresThrough(ctx context.Context, db *sql.DB, names []string) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize initial provisioning as well as subsequent migrations.
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '10s';
		SELECT pg_catalog.pg_advisory_xact_lock(1196380498, 1);
		CREATE SCHEMA IF NOT EXISTS gomar;
		SET LOCAL search_path TO gomar, pg_catalog;
		CREATE TABLE IF NOT EXISTS gomar.schema_migrations (
			version INTEGER PRIMARY KEY CHECK (version > 0),
			name TEXT NOT NULL UNIQUE,
			digest TEXT NOT NULL CHECK (length(digest) = 64)
		);`); err != nil {
		return fmt.Errorf("prepare PostgreSQL migrations: %w", err)
	}
	type migration struct{ name, digest string }
	var installed []migration
	rows, err := tx.QueryContext(ctx, `SELECT version, name, digest FROM gomar.schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var version int
		var row migration
		if err := rows.Scan(&version, &row.name, &row.digest); err != nil {
			rows.Close()
			return err
		}
		if version != len(installed)+1 || version > len(names) {
			rows.Close()
			return errors.New("unknown or incomplete PostgreSQL migration history")
		}
		installed = append(installed, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i, name := range names {
		script, err := postgresMigrations.ReadFile("postgres_migrations/" + name)
		if err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(script))
		if i < len(installed) {
			if installed[i] != (migration{name, digest}) {
				return fmt.Errorf("PostgreSQL migration does not match the installed version: %s", name)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			return fmt.Errorf("apply PostgreSQL migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO gomar.schema_migrations(version,name,digest) VALUES ($1,$2,$3)`, i+1, name, digest); err != nil {
			return err
		}
	}
	return tx.Commit()
}
