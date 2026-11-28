package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgresStagedIntakeMaintenanceLimits(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'synthetic-intake','unused',TRUE,FALSE,FALSE);
		INSERT INTO entities(id,name,cuit,active) VALUES(1,'One','00000000001',TRUE),(2,'Two','00000000002',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'One',TRUE),(2,2,'Two',TRUE)`)
	seed := func(entity, count, size int) {
		t.Helper()
		exec(`INSERT INTO intake_operations(id,entity_id,outlet_id,user_id,status,mime_type,size,digest,storage_name,created_at,expires_at)
		SELECT id,$1,$1,1,'expired','image/png',$3,repeat('0',64),'intake-'||id,1,2 FROM (
		  SELECT '00000000-0000-4000-8000-'||lpad(i::text,12,'0') AS id FROM generate_series(1,$2::int) i
		) rows`, entity, count, size)
	}
	check := func(entity, size int64, want error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := requireIntakeCapacity(ctx, tx, entity, size); err != want {
			t.Fatalf("entity %d upload size %d: got %v, want %v", entity, size, err, want)
		}
	}
	seed(1, intakeEntityFiles, 1)
	check(1, 1, intakeStorageBudget)
	check(2, 1, nil)
	exec(`UPDATE intake_operations SET storage_name=NULL WHERE id='00000000-0000-4000-8000-000000000001'`)
	check(1, 1, nil)
	exec("DELETE FROM intake_operations")
	seed(1, intakeEntityBytes/maxReceiptBytes, maxReceiptBytes)
	check(1, 1, intakeStorageBudget)
	check(2, 1, nil)
	exec("DELETE FROM intake_operations")
	seed(1, intakeServiceFiles, 1)
	check(2, 1, intakeStorageBudget)
	exec("DELETE FROM intake_operations")
	seed(1, intakeServiceBytes/maxReceiptBytes+1, maxReceiptBytes)
	exec(`UPDATE intake_operations SET size=$1 WHERE id='00000000-0000-4000-8000-000000000001'`, intakeServiceBytes%maxReceiptBytes)
	check(2, maxReceiptBytes, intakeStorageBudget)
	exec("DELETE FROM intake_operations")

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &app{db: db, privateRoot: root}
	seed(1, intakeSweepBatch+1, 1)
	badID := fmt.Sprintf("00000000-0000-4000-8000-%012d", 1)
	if err := os.WriteFile(filepath.Join(dir, intakeStorage(badID)), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	cursor, err := a.sweepStagedIntake(ctx, "", 3)
	if err == nil || cursor != fmt.Sprintf("00000000-0000-4000-8000-%012d", intakeSweepBatch) {
		t.Fatalf("first sweep did not advance past corrupt source: %q, %v", cursor, err)
	}
	if cursor, err = a.sweepStagedIntake(ctx, cursor, 3); cursor != "" || err != nil {
		t.Fatalf("next sweep did not process the final source: %q, %v", cursor, err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM intake_operations WHERE storage_name IS NOT NULL").Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("invalid source prevented other cleanup: %d, %v", remaining, err)
	}
	if _, err := os.Stat(filepath.Join(dir, intakeStorage(badID))); err != nil {
		t.Fatal("corrupt file was unlinked without verification", err)
	}
}
