package main

import (
	"database/sql"
	"testing"
)

func TestPostgresMCPReconciliationDraftAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO users(id,username,password,active,staff,superuser) VALUES
 (1,'reconciliation-fixture','inert',TRUE,FALSE,FALSE),(2,'reader-fixture','inert',TRUE,FALSE,FALSE);
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'read_only',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(2,1)`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db}
	scope := reconciliationDraftScope{1, 1, 1}
	in := reconciliationDraftInput{MatchReference: true}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	p, err := prepareReconciliationDraft(tx, scope, in)
	if err != nil || p.Scope != scope || !p.Input.MatchReference {
		t.Fatal(p, err)
	}
	for _, denied := range []reconciliationDraftScope{{2, 1, 1}, {1, 1, 2}} {
		if _, err := prepareReconciliationDraft(tx, denied, in); err == nil {
			t.Fatal("unavailable scope accepted")
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		var runs, links, audits, marker, sources int
		err := db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM reconciliation_read_runs WHERE entity_id=1 AND revision=1 AND state='draft' AND match_reference),
 (SELECT count(*) FROM reconciliation_read_outlets WHERE outlet_id=1),
 (SELECT count(*) FROM reconciliation_detail_events WHERE ordinal=1 AND actor_id=1),
 (SELECT count(*) FROM reconciliation_native_v1_runs),
 (SELECT count(*) FROM reconciliation_read_sources)`).Scan(&runs, &links, &audits, &marker, &sources)
		if err != nil || runs != want || links != want || audits != want || marker != want || sources != 0 {
			t.Fatal(runs, links, audits, marker, sources, err)
		}
	}
	check(0)
	apply := func(preview reconciliationDraftPreview, actor int64, approval [32]byte, commit bool) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		run, err := a.applyReconciliationDraftApproval(ctx, tx, user{id: actor}, preview.Scope, preview, approval)
		if err != nil {
			return err
		}
		if run.ID <= 0 || len(run.Outlets) != 1 || run.Outlets[0] != 1 || !run.MatchReference {
			t.Fatal(run)
		}
		if commit {
			return tx.Commit()
		}
		return nil
	}
	if err := apply(p, 2, p.digest(), false); err == nil {
		t.Fatal("different actor accepted")
	}
	changed := p
	changed.Input.MatchReference = false
	if err := apply(changed, 1, p.digest(), false); err == nil {
		t.Fatal("different snapshot accepted")
	}
	if err := apply(p, 1, p.digest(), false); err != nil {
		t.Fatal(err)
	}
	check(0)
	if err := apply(p, 1, p.digest(), true); err != nil {
		t.Fatal(err)
	}
	check(1)
	// Dedup is the future approval-row lock's job. Replaying the input in
	// a rolled-back transaction cannot leave a second draft or audit.
	if err := apply(p, 1, p.digest(), false); err != nil {
		t.Fatal(err)
	}
	check(1)
}
