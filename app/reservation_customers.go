package main

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"github.com/nyaruka/phonenumbers/v2"
)

var reservationPhoneInput = regexp.MustCompile(`^\+[1-9][0-9 ().-]{1,31}$`)
var reservationPhoneE164 = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)
var reservationPhoneGrouping = strings.NewReplacer(" ", "", "(", "", ")", "", ".", "", "-", "")

func normalizeReservationPhone(raw string) (string, bool) {
	if len(raw) > 48 {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	if !reservationPhoneInput.MatchString(raw) {
		return "", false
	}
	n, err := phonenumbers.Parse(raw, "ZZ")
	if err != nil || !phonenumbers.IsValidNumber(n) {
		return "", false
	}
	phone := phonenumbers.Format(n, phonenumbers.E164)
	digits := reservationPhoneGrouping.Replace(raw)
	// No national dialing prefixes, guessed regions, or silent rewrites.
	return phone, phone == digits && reservationPhoneE164.MatchString(phone)
}

// Contact writes maintain entity → outlet lock order. Taking only a key-share
// lock on the entity lets independent outlets keep accepting bookings.
func lockReservationContactEntity(ctx context.Context, tx *sql.Tx, venue string) (int64, error) {
	outlet, _, err := reservationPublicVenue(ctx, tx, venue)
	if err != nil {
		return 0, err
	}
	var entity int64
	err = tx.QueryRowContext(ctx, `SELECT e.id FROM entities e JOIN outlets o ON o.entity_id=e.id
		WHERE o.id=$1 AND o.active AND e.active FOR KEY SHARE OF e`, outlet).Scan(&entity)
	if err == sql.ErrNoRows {
		return 0, reservationUnavailable
	}
	return outlet, err
}

func reserveCustomerContact(ctx context.Context, tx *sql.Tx, outlet int64, reservation, phone, note string) error {
	var entity int64
	if err := tx.QueryRowContext(ctx, `SELECT entity_id FROM outlets WHERE id=$1 AND active`, outlet).Scan(&entity); err != nil {
		return err
	}
	// A matched number is not proof of ownership. Never overwrite a profile
	// or return a match result. Concurrent requests in different branches
	// converge on one customer under the organization-scoped unique key.
	if _, err := tx.ExecContext(ctx, `INSERT INTO reservation_customers(entity_id,phone_e164) VALUES($1,$2)
		ON CONFLICT(entity_id,phone_e164) DO NOTHING`, entity, phone); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO reservation_private_contacts(reservation_id,outlet_id,entity_id,customer_id,note)
		SELECT $1,$2,entity_id,id,$3 FROM reservation_customers WHERE entity_id=$4 AND phone_e164=$5`,
		reservation, outlet, note, entity, phone)
	return err
}
