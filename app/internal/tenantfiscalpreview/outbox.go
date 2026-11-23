package tenantfiscalpreview

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"time"
)

//go:embed outbox.sql
var fixtureSchema string

var (
	ErrFixtureDB  = errors.New("a new disposable fiscal fixture database is required")
	ErrBusy       = errors.New("fiscal fixture channel has a held number")
	ErrNoWork     = errors.New("no queued fiscal fixture intent")
	ErrStaleClaim = errors.New("fiscal fixture claim is stale")
)

// Outbox is test infrastructure. It does not grant permissions or perform I/O
// with a fiscal provider. App startup cannot install this fixture schema.
type Outbox struct{ db *sql.DB }

// BootstrapOutbox installs a new synthetic schema only in a database named for
// Gomar QA. A trusted caller must supply a disposable database. Use OpenOutbox
// after a restart; it does not reinstall or reset state.
func BootstrapOutbox(ctx context.Context, db *sql.DB) (*Outbox, error) {
	if err := fixtureDatabase(ctx, db); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, fixtureSchema); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Outbox{db: db}, nil
}

func OpenOutbox(ctx context.Context, db *sql.DB) (*Outbox, error) {
	if err := fixtureDatabase(ctx, db); err != nil {
		return nil, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT pg_catalog.to_regclass('gomar_fiscal_fixture.intents') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrFixtureDB
	}
	return &Outbox{db: db}, nil
}

func fixtureDatabase(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrFixtureDB
	}
	var fixture bool
	if err := db.QueryRowContext(ctx, `SELECT pg_catalog.current_database() ~ '^gomar_qa_[a-z0-9_]+$'`).Scan(&fixture); err != nil {
		return err
	}
	if !fixture {
		return ErrFixtureDB
	}
	return nil
}

func (o *Outbox) Configure(ctx context.Context, p Pilot) error {
	if !p.Enabled || p.Scope.EntityID < 1 || p.Scope.OutletID < 1 || !fixtureIssuer.MatchString(p.Issuer) || p.PointOfSale < 1 || p.PointOfSale > maxPointOfSale {
		return ErrUnavailable
	}
	_, err := o.db.ExecContext(ctx, `INSERT INTO gomar_fiscal_fixture.channels (entity,outlet,issuer,point) VALUES ($1,$2,$3,$4)`, p.Scope.EntityID, p.Scope.OutletID, p.Issuer, p.PointOfSale)
	return err
}

// Enqueue requires the domain's human-confirmed snapshot. Replays of that
// exact input return the same intent; a different actor, amount or date cannot
// rewrite it. Production callers would have to recheck document grants in their
// own authoritative transaction before calling this primitive.
func (o *Outbox) Enqueue(ctx context.Context, c Confirmed) (int64, error) {
	p := c.preview
	if !p.valid() {
		return 0, ErrAttempt
	}
	var id int64
	err := o.db.QueryRowContext(ctx, `INSERT INTO gomar_fiscal_fixture.intents
 (entity,outlet,issuer,point,sale,actor,total,issue_date)
 SELECT c.entity,c.outlet,c.issuer,c.point,$5,$6,$7,$8 FROM gomar_fiscal_fixture.channels c
 WHERE c.entity=$1 AND c.outlet=$2 AND c.issuer=$3 AND c.point=$4
 ON CONFLICT (entity,outlet,sale) DO UPDATE SET sale=excluded.sale
 WHERE intents.actor=excluded.actor AND intents.total=excluded.total AND intents.issue_date=excluded.issue_date
 RETURNING id`, p.pilot.Scope.EntityID, p.pilot.Scope.OutletID, p.pilot.Issuer, p.pilot.PointOfSale, p.sale.ID, p.operator.ActorID, p.sale.TotalMinor, p.sale.OperationDate).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUnavailable
	}
	return id, err
}

type Claim struct {
	ID      int64
	Attempt Attempt
	token   string
}
type rowScanner interface{ Scan(...any) error }

func obtain(p Pilot, row rowScanner) (int64, Attempt, error) {
	var id, sa, actor, total int64
	var date string
	var number int
	if err := row.Scan(&id, &sa, &actor, &total, &date, &number); err != nil {
		return 0, Attempt{}, err
	}
	operator := Operator{Scope: p.Scope, ActorID: actor, MayIssue: true}
	sale := Sale{Scope: p.Scope, ID: sa, TotalMinor: total, OperationDate: date, OnlineRecorded: true, Due: true}
	preview, err := Prepare(p, operator, sale)
	if err != nil {
		return 0, Attempt{}, err
	}
	attempt, err := (Confirmed{preview: preview}).WithReservedNumber(number)
	return id, attempt, err
}

func lockChannel(ctx context.Context, tx *sql.Tx, p Pilot) (int, error) {
	if !p.Enabled {
		return 0, ErrUnavailable
	}
	var number int
	err := tx.QueryRowContext(ctx, `SELECT next_number FROM gomar_fiscal_fixture.channels
 WHERE entity=$1 AND outlet=$2 AND issuer=$3 AND point=$4 FOR UPDATE`, p.Scope.EntityID, p.Scope.OutletID, p.Issuer, p.PointOfSale).Scan(&number)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUnavailable
	}
	return number, err
}

// ClaimNext commits the sole claim before returning. Loss of a worker, whether
// before or after dispatch, leaves this number held. No worker can re-claim it.
func (o *Outbox) ClaimNext(ctx context.Context, p Pilot, lease time.Duration) (Claim, error) {
	if lease < time.Second || lease > time.Hour {
		return Claim{}, ErrAttempt
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback()
	n, err := lockChannel(ctx, tx, p)
	if err != nil {
		return Claim{}, err
	}
	args := []any{p.Scope.EntityID, p.Scope.OutletID}
	if _, err = tx.ExecContext(ctx, `UPDATE gomar_fiscal_fixture.intents
 SET state='unknown',claim=NULL,expires_at=NULL
 WHERE entity=$1 AND outlet=$2 AND state='claimed' AND expires_at<=clock_timestamp()`, args...); err != nil {
		return Claim{}, err
	}
	var held bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT FROM gomar_fiscal_fixture.intents
 WHERE entity=$1 AND outlet=$2 AND state IN ('claimed','unknown','refused','review'))`, args...).Scan(&held); err != nil {
		return Claim{}, err
	}
	if held {
		if err = tx.Commit(); err != nil {
			return Claim{}, err
		}
		return Claim{}, ErrBusy
	}
	token := hex.EncodeToString(randBytes())
	if n > maxNumber {
		return Claim{}, ErrBusy
	}
	args = append(args, n, token, lease.Microseconds())
	id, attempt, err := obtain(p, tx.QueryRowContext(ctx, `UPDATE gomar_fiscal_fixture.intents
 SET state='claimed',number=$3,claim=$4,expires_at=clock_timestamp()+($5::bigint*interval '1 microsecond')
 WHERE id=(SELECT id FROM gomar_fiscal_fixture.intents WHERE entity=$1 AND outlet=$2 AND state='queued' ORDER BY id LIMIT 1)
 RETURNING id,sale,actor,total,to_char(issue_date,'YYYY-MM-DD'),number`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, ErrNoWork
	}
	if err != nil {
		return Claim{}, err
	}
	if err = tx.Commit(); err != nil {
		return Claim{}, err
	}
	return Claim{ID: id, Attempt: attempt, token: token}, nil
}

func randBytes() []byte {
	var b [16]byte
	rand.Read(b[:])
	return b[:]
}

// UnknownAttempt reads the exact held request to look up. It never issues a
// new claim and never returns a request from another fixture channel.
func (o *Outbox) UnknownAttempt(ctx context.Context, p Pilot, id int64) (Attempt, error) {
	if !p.Enabled {
		return Attempt{}, ErrUnavailable
	}
	_, attempt, err := obtain(p, o.db.QueryRowContext(ctx, `SELECT id,sale,actor,total,to_char(issue_date,'YYYY-MM-DD'),number
 FROM gomar_fiscal_fixture.intents WHERE entity=$1 AND outlet=$2 AND issuer=$3 AND point=$4 AND id=$5 AND state='unknown'`,
		p.Scope.EntityID, p.Scope.OutletID, p.Issuer, p.PointOfSale, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, ErrUnavailable
	}
	return attempt, err
}

func (o *Outbox) AfterDispatch(ctx context.Context, c Claim, reply Reply) (Status, error) {
	if c.token == "" || c.ID < 1 {
		return NoStatus, ErrStaleClaim
	}
	return o.persist(ctx, c.ID, c.Attempt, c.token, reply)
}

func (o *Outbox) Reconcile(ctx context.Context, id int64, a Attempt, reply Reply) (Status, error) {
	return o.persist(ctx, id, a, "", reply)
}

func (o *Outbox) persist(ctx context.Context, id int64, attempt Attempt, token string, reply Reply) (Status, error) {
	if id < 1 || !attempt.confirmed.preview.valid() {
		return NoStatus, ErrAttempt
	}
	p := attempt.confirmed.preview.pilot
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return NoStatus, err
	}
	defer tx.Rollback()
	next, err := lockChannel(ctx, tx, p)
	if err != nil {
		return NoStatus, err
	}
	if next != attempt.payload.Key.Number {
		return NoStatus, ErrStaleClaim
	}
	_, stored, err := obtain(p, tx.QueryRowContext(ctx, `SELECT id,sale,actor,total,to_char(issue_date,'YYYY-MM-DD'),number
 FROM gomar_fiscal_fixture.intents WHERE entity=$1 AND outlet=$2 AND id=$3
 AND (($4 <> '' AND state='claimed' AND claim=$4 AND expires_at>clock_timestamp())
 OR ($4 = '' AND state='unknown')) FOR UPDATE`, p.Scope.EntityID, p.Scope.OutletID, id, token))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && stored != attempt) {
		return NoStatus, ErrStaleClaim
	}
	if err != nil {
		return NoStatus, err
	}
	var decision Decision
	if token == "" {
		decision, err = AfterLookup(Decision{attempt: stored, status: Unknown}, stored, reply)
	} else {
		decision, err = AfterSubmit(stored, reply)
	}
	if err != nil {
		return NoStatus, err
	}
	var state string
	var code, expiry any
	switch decision.Status() {
	case Authorized:
		state = "authorized"
		code, expiry, _ = decision.Authorization()
		if _, err = tx.ExecContext(ctx, `UPDATE gomar_fiscal_fixture.channels SET next_number=next_number+1
  WHERE entity=$1 AND outlet=$2`, p.Scope.EntityID, p.Scope.OutletID); err != nil {
			return NoStatus, err
		}
	case Refused:
		state = "refused"
	case NeedsReview:
		state = "review"
	case Unknown:
		state = "unknown"
	default:
		return NoStatus, ErrAttempt
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gomar_fiscal_fixture.intents SET state=$1,code=$2,code_expiry=$3,claim=NULL,expires_at=NULL WHERE id=$4`, state, code, expiry, id); err != nil {
		return NoStatus, err
	}
	if err = tx.Commit(); err != nil {
		return NoStatus, err
	}
	return decision.Status(), nil
}
