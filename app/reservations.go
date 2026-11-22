package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	reservationLocale       = "es-AR"
	reservationTimezone     = "America/Argentina/Buenos_Aires"
	reservationMinutes      = 90
	reservationHorizon      = 60
	reservationPendingLimit = 20
)

var (
	reservationLocation      = payablesLocation
	reservationSlug          = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	reservationID            = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reservationUnavailable   = problem{404, "No se encontraron reservas para este local."}
	reservationMissing       = problem{404, "No se encontró la solicitud."}
	reservationConflict      = problem{409, "Ese horario ya no está disponible."}
	reservationStateConflict = problem{409, "La solicitud ya no se puede cambiar."}
	reservationQueueFull     = problem{429, "Ya hay varias solicitudes para ese horario. Probá con otro."}
)

type reservationVenue struct {
	Slug               string `json:"venue"`
	DisplayName        string `json:"display_name"`
	Locale             string `json:"locale"`
	Timezone           string `json:"timezone"`
	CancellationPolicy string `json:"cancellation_policy"`
	MenuPDFURL         string `json:"menu_pdf_url,omitempty"`
}
type reservationSettings struct {
	reservationVenue
	Enabled bool `json:"enabled"`
}
type reservationSlot struct {
	StartAt string `json:"start_at"`
}
type reservationInput struct {
	GuestName string `json:"guest_name"`
	PartySize int    `json:"party_size"`
	StartAt   string `json:"start_at"`
}
type reservationResult struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	TableLabel         string  `json:"table_label,omitempty"`
	CancellationPolicy *string `json:"cancellation_policy,omitempty"`
}
type reservationEntry struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	TableLabel         string  `json:"table_label"`
	CancellationPolicy *string `json:"cancellation_policy"`
	reservationInput
}
type reservationTable struct {
	Label    string `json:"label"`
	Capacity int    `json:"capacity"`
}
type reservationsService struct {
	db *sql.DB
}

func reservationValidSlug(slug string) bool {
	return len(slug) <= 48 && reservationSlug.MatchString(slug)
}
func reservationText(value string, min, max int) (string, bool) {
	value = strings.TrimSpace(value)
	n := utf8.RuneCountInString(value)
	return value, utf8.ValidString(value) && n >= min && n <= max && strings.IndexFunc(value, unicode.IsControl) < 0
}
func reservationDay(raw string) (time.Time, error) {
	day, err := time.ParseInLocation("2006-01-02", raw, reservationLocation)
	if err != nil || day.Format("2006-01-02") != raw || day.Year() < 2000 || day.Year() > 9998 {
		return time.Time{}, badInput
	}
	return day, nil
}
func reservationBookableDay(day, now time.Time) bool {
	localNow := now.In(reservationLocation)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, reservationLocation)
	return !day.Before(today) && !day.After(today.AddDate(0, 0, reservationHorizon))
}
func reservationStart(raw string, now time.Time) (string, error) {
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil || at.Format(time.RFC3339) != raw || !at.After(now) {
		return "", badInput
	}
	local := at.In(reservationLocation)
	if local.Second() != 0 || local.Nanosecond() != 0 || (local.Hour()*60+local.Minute())%reservationMinutes != 0 {
		return "", badInput
	}
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, reservationLocation)
	if !reservationBookableDay(day, now) {
		return "", badInput
	}
	return at.UTC().Format(time.RFC3339), nil
}
func reservationPublicVenue(ctx context.Context, tx *sql.Tx, slug string) (int64, reservationVenue, error) {
	var outletID int64
	v := reservationVenue{Locale: reservationLocale, Timezone: reservationTimezone}
	if !reservationValidSlug(slug) {
		return 0, v, reservationUnavailable
	}
	err := tx.QueryRowContext(ctx, `SELECT v.outlet_id,v.slug,v.display_name,v.cancellation_policy
        FROM reservation_venues v JOIN outlets o ON o.id=v.outlet_id AND o.active
        JOIN entities e ON e.id=o.entity_id AND e.active WHERE v.slug=$1 AND v.enabled`, slug).
		Scan(&outletID, &v.Slug, &v.DisplayName, &v.CancellationPolicy)
	if errors.Is(err, sql.ErrNoRows) {
		err = reservationUnavailable
	}
	return outletID, v, err
}

// lockedVenue returns the public outlet after locking and re-reading its
// venue settings. A remapped slug cannot move a mutation under a different
// outlet's lock. SQLite's IMMEDIATE transaction already serializes writes.
func (s reservationsService) lockedVenue(ctx context.Context, tx *sql.Tx, slug string) (int64, error) {
	outlet, _, err := reservationPublicVenue(ctx, tx, slug)
	if err != nil {
		return outlet, err
	}
	if err = lockPostgresOutlet(ctx, tx, outlet); err != nil {
		return 0, err
	}
	current, _, err := reservationPublicVenue(ctx, tx, slug)
	if err != nil {
		return 0, err
	}
	if current != outlet {
		return 0, reservationUnavailable
	}
	return outlet, nil
}

func (s reservationsService) info(ctx context.Context, venue string) (reservationVenue, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return reservationVenue{}, err
	}
	defer tx.Rollback()
	_, result, err := reservationPublicVenue(ctx, tx, venue)
	return result, err
}

const availableReservationTable = ` NOT EXISTS(SELECT 1 FROM reservation_requests r
    WHERE r.outlet_id=t.outlet_id AND r.start_at=s.start_at AND r.table_label=t.label AND r.status='confirmed')`

func (s reservationsService) availability(ctx context.Context, venue, date string, partySize int, now time.Time) ([]reservationSlot, error) {
	day, err := reservationDay(date)
	if err != nil || partySize < 1 || partySize > 12 || !reservationBookableDay(day, now) {
		return nil, badInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, _, err := reservationPublicVenue(ctx, tx, venue)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.start_at FROM reservation_slots s
        WHERE s.outlet_id=$1 AND s.start_at>=$2 AND s.start_at<$3 AND s.start_at>$4
        AND EXISTS(SELECT 1 FROM reservation_tables t WHERE t.outlet_id=s.outlet_id AND t.capacity>=$5 AND `+availableReservationTable+`)
        ORDER BY s.start_at`, id, day.UTC().Format(time.RFC3339), day.AddDate(0, 0, 1).UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), partySize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []reservationSlot{}
	for rows.Next() {
		var slot reservationSlot
		if err := rows.Scan(&slot.StartAt); err != nil {
			return nil, err
		}
		result = append(result, slot)
	}
	return result, rows.Err()
}
func pickReservationTable(ctx context.Context, tx *sql.Tx, outlet int64, start string, partySize int) (string, error) {
	label, _, err := pickReservationTableWithCapacity(ctx, tx, outlet, start, partySize)
	return label, err
}

func pickReservationTableWithCapacity(ctx context.Context, tx *sql.Tx, outlet int64, start string, partySize int) (string, int, error) {
	var table string
	var capacity int
	err := tx.QueryRowContext(ctx, `SELECT t.label,t.capacity FROM reservation_slots s
        JOIN reservation_tables t ON t.outlet_id=s.outlet_id WHERE s.outlet_id=$1 AND s.start_at=$2
        AND t.capacity>=$3 AND `+availableReservationTable+` ORDER BY t.capacity,t.label LIMIT 1`, outlet, start, partySize).Scan(&table, &capacity)
	if errors.Is(err, sql.ErrNoRows) {
		err = reservationConflict
	}
	return table, capacity, err
}
func (s reservationsService) request(ctx context.Context, venue string, in reservationInput, now time.Time) (reservationResult, error) {
	start, err := reservationStart(in.StartAt, now)
	name, valid := reservationText(in.GuestName, 1, 80)
	if err != nil || !valid || in.PartySize < 1 || in.PartySize > 12 {
		return reservationResult{}, badInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return reservationResult{}, err
	}
	defer tx.Rollback()
	outlet, err := s.lockedVenue(ctx, tx, venue)
	if err != nil {
		return reservationResult{}, err
	}
	result, err := requestReservationInTx(ctx, tx, outlet, start, name, in.PartySize, now)
	if err != nil {
		return reservationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return reservationResult{}, err
	}
	return result, nil
}

func requestReservationInTx(ctx context.Context, tx *sql.Tx, outlet int64, start, name string, partySize int, now time.Time) (reservationResult, error) {
	if _, err := pickReservationTable(ctx, tx, outlet, start, partySize); err != nil {
		return reservationResult{}, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM reservation_requests
        WHERE outlet_id=$1 AND start_at=$2 AND status='pending'`, outlet, start).Scan(&pending); err != nil {
		return reservationResult{}, err
	}
	if pending >= reservationPendingLimit {
		return reservationResult{}, reservationQueueFull
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return reservationResult{}, err
	}
	id := hex.EncodeToString(random[:])
	_, err := tx.ExecContext(ctx, `INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,created_at)
        VALUES($1,$2,$3,$4,$5,'pending',$6)`, id, outlet, start, name, partySize, now.UTC().Format(time.RFC3339))
	if err != nil {
		return reservationResult{}, err
	}
	policy, err := reservationSubmittedPolicy(ctx, tx, id)
	if err != nil {
		return reservationResult{}, err
	}
	return reservationResult{ID: id, Status: "pending", CancellationPolicy: policy}, nil
}

func reservationChange(ctx context.Context, tx *sql.Tx, outlet int64, id, action string, now time.Time) (reservationResult, error) {
	var status, start string
	var partySize int
	if !reservationID.MatchString(id) {
		return reservationResult{}, reservationMissing
	}
	err := tx.QueryRowContext(ctx, `SELECT status,start_at,party_size FROM reservation_requests WHERE outlet_id=$1 AND id=$2`, outlet, id).Scan(&status, &start, &partySize)
	if errors.Is(err, sql.ErrNoRows) {
		err = reservationMissing
	}
	if err != nil {
		return reservationResult{}, err
	}
	if action == "cancel" {
		if status != "cancelled" {
			_, err = tx.ExecContext(ctx, `UPDATE reservation_requests SET status='cancelled',table_label=NULL WHERE outlet_id=$1 AND id=$2`, outlet, id)
		}
		return reservationResult{ID: id, Status: "cancelled"}, err
	}
	var enabled bool
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM reservation_venues WHERE outlet_id=$1`, outlet).Scan(&enabled); err != nil {
		return reservationResult{}, err
	}
	if action != "confirm" || status != "pending" || !enabled || start <= now.UTC().Format(time.RFC3339) {
		return reservationResult{}, reservationStateConflict
	}
	label, err := pickReservationTable(ctx, tx, outlet, start, partySize)
	if err != nil {
		return reservationResult{}, err
	}
	return confirmReservationTable(ctx, tx, outlet, id, start, partySize, label)
}

func confirmReservationTable(ctx context.Context, tx *sql.Tx, outlet int64, id, start string, partySize int, label string) (reservationResult, error) {
	updated, err := tx.ExecContext(ctx, `UPDATE reservation_requests SET status='confirmed',table_label=$1 WHERE outlet_id=$2 AND id=$3 AND status='pending' AND start_at=$4 AND party_size=$5`, label, outlet, id, start, partySize)
	if err != nil {
		if reservationConflictError(err) {
			return reservationResult{}, reservationConflict
		}
		return reservationResult{}, err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return reservationResult{}, err
	}
	if n != 1 {
		return reservationResult{}, reservationStateConflict
	}
	return reservationResult{ID: id, Status: "confirmed", TableLabel: label}, nil
}
