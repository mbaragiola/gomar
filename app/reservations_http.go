package main

import (
	"context"
	"database/sql"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"
)

const (
	reservationPublicBase = "/api/v1/public/reservas/{venue}"
	reservationStaffBase  = "/api/v1/entities/{entity}/outlets/{outlet}/reservations"
)

func (a *app) registerReservations(add func(string, string, http.HandlerFunc), publicEnabled bool) {
	a.registerReservationPublic(add, publicEnabled, time.Now)
	if a.reservationContacts && publicEnabled {
		add(reservationStaffBase+"/contact-settings", "GET", a.reservationStaff(reservationContactSettingsRead, false))
		add(reservationStaffBase+"/contact-settings", "PUT", a.reservationStaff(reservationContactSettingsSave, true))
	}
	add(reservationStaffBase, "GET", a.reservationStaff(listReservationRequests, false))
	add(reservationStaffBase+"/settings", "GET", a.reservationStaff(reservationSettingsRead, false))
	add(reservationStaffBase+"/settings", "PUT", a.reservationStaff(reservationSettingsSave, true))
	add(reservationStaffBase+"/tables", "POST", a.reservationStaff(reservationTableCreate, true))
	add(reservationStaffBase+"/slots", "POST", a.reservationStaff(reservationSlotCreate, true))
	for _, action := range []string{"confirm", "cancel"} {
		add(reservationStaffBase+"/{id}/"+action, "POST", a.reservationStaff(func(r *http.Request, tx *sql.Tx, outlet int64, _ []byte) (int, any, error) {
			out, err := reservationChange(r.Context(), tx, outlet, r.PathValue("id"), action, time.Now())
			return http.StatusOK, out, err
		}, false))
	}
}
func (a *app) registerReservationPublic(add func(string, string, http.HandlerFunc), publicEnabled bool, clock func() time.Time) {
	s := reservationsService{db: a.db}
	serve := func(fn func(http.ResponseWriter, *http.Request, string) (int, any, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if !publicEnabled {
				failure(w, reservationUnavailable)
				return
			}
			code, out, err := fn(w, r, r.PathValue("venue"))
			if err != nil {
				failure(w, err)
				return
			}
			jsonResponse(w, code, out)
		}
	}
	add(reservationPublicBase, "GET", serve(func(_ http.ResponseWriter, r *http.Request, venue string) (int, any, error) {
		v, err := s.info(r.Context(), venue)
		if err != nil {
			return 0, nil, err
		}
		v.CollectContact = a.reservationContacts && v.CollectContact
		if err != nil {
			return 0, nil, err
		}
		menu, err := reservationMenuForConcierge(r.Context(), a.db, venue, publicEnabled)
		if menu != nil {
			v.MenuPDFURL = menu.Path
		}
		return 200, v, err
	}))
	add(reservationPublicBase+"/availability", "GET", serve(func(_ http.ResponseWriter, r *http.Request, venue string) (int, any, error) {
		query := r.URL.Query()
		if len(query["date"]) != 1 || len(query["party_size"]) != 1 {
			return 0, nil, badInput
		}
		n, err := strconv.Atoi(query.Get("party_size"))
		if err != nil {
			return 0, nil, badInput
		}
		slots, err := s.availability(r.Context(), venue, query.Get("date"), n, clock())
		return 200, struct {
			Slots []reservationSlot `json:"slots"`
		}{slots}, err
	}))
	add(reservationPublicBase+"/requests", "POST", serve(func(w http.ResponseWriter, r *http.Request, venue string) (int, any, error) {
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			return 0, nil, problem{415, "Enviá los datos en formato JSON."}
		}
		var in reservationPublicInput
		if err := parseJSON(w, r, &in); err != nil {
			return 0, nil, err
		}
		result, err := s.requestPublic(r.Context(), venue, in, clock(), a.reservationContacts)
		return 201, result, err
	}))
}

func reservationContactSettingsRead(r *http.Request, tx *sql.Tx, outlet int64, _ []byte) (int, any, error) {
	var collect bool
	err := tx.QueryRowContext(r.Context(), `SELECT collect_contact FROM reservation_venues WHERE outlet_id=$1`, outlet).Scan(&collect)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, reservationUnavailable
	}
	return 200, map[string]bool{"collect_contact": collect}, err
}

func reservationContactSettingsSave(r *http.Request, tx *sql.Tx, outlet int64, body []byte) (int, any, error) {
	var in struct {
		Collect *bool `json:"collect_contact"`
	}
	if err := decodeJSON(body, &in); err != nil {
		return 0, nil, err
	}
	if in.Collect == nil {
		return 0, nil, badInput
	}
	var collect bool
	if err := tx.QueryRowContext(r.Context(), `UPDATE reservation_venues SET collect_contact=$2 WHERE outlet_id=$1 RETURNING collect_contact`, outlet, *in.Collect).Scan(&collect); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, reservationUnavailable
		}
		return 0, nil, err
	}
	return 200, map[string]bool{"collect_contact": collect}, nil
}

type reservationStaffHandler func(*http.Request, *sql.Tx, int64, []byte) (int, any, error)

func (a *app) reservationStaff(next reservationStaffHandler, readBody bool) http.HandlerFunc {
	handler := a.scopedRequest("", func(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
		outlet, err := pathID(r, "outlet")
		if err != nil {
			return 0, nil, err
		}
		if err := requireReservationStaff(r.Context(), tx, m, outlet); err != nil {
			return 0, nil, err
		}
		if !safeMethod(r.Method) {
			if err := lockPostgresOutlet(r.Context(), tx, outlet); err != nil {
				return 0, nil, err
			}
			fresh, err := scopedMembership(tx, m.userID, m.ID, "")
			if err != nil {
				return 0, nil, err
			}
			if err := requireReservationStaff(r.Context(), tx, fresh, outlet); err != nil {
				return 0, nil, err
			}
		}
		return next(r, tx, outlet, body)
	}, readBody)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		handler(w, r)
	}
}

func requireReservationStaff(ctx context.Context, tx *sql.Tx, m entityMembership, outlet int64) error {
	// Keep reservation operation separate from financial capabilities.
	if m.role != "administration" {
		return forbiddenRole
	}
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM outlets o JOIN outlet_grants g
        ON g.outlet_id=o.id WHERE g.membership_id=$1 AND o.id=$2 AND o.entity_id=$3 AND o.active)`, m.id, outlet, m.ID).Scan(&allowed)
	if err == nil && !allowed {
		return missingOutlet
	}
	return err
}

func reservationSettingsRead(r *http.Request, tx *sql.Tx, outlet int64, _ []byte) (int, any, error) {
	v := reservationSettings{reservationVenue: reservationVenue{Locale: reservationLocale, Timezone: reservationTimezone}}
	err := tx.QueryRowContext(r.Context(), `SELECT slug,display_name,cancellation_policy,enabled
        FROM reservation_venues WHERE outlet_id=$1`, outlet).Scan(&v.Slug, &v.DisplayName, &v.CancellationPolicy, &v.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		err = reservationUnavailable
	}
	return 200, v, err
}
func reservationSettingsSave(r *http.Request, tx *sql.Tx, outlet int64, body []byte) (int, any, error) {
	var input struct {
		Slug               string `json:"venue"`
		DisplayName        string `json:"display_name"`
		CancellationPolicy string `json:"cancellation_policy"`
		Enabled            *bool  `json:"enabled"`
	}
	if err := decodeJSON(body, &input); err != nil {
		return 0, nil, err
	}
	name, nameOK := reservationText(input.DisplayName, 1, 80)
	policy, policyOK := reservationText(input.CancellationPolicy, 0, 500)
	if !reservationValidSlug(input.Slug) || !nameOK || !policyOK || input.Enabled == nil {
		return 0, nil, badInput
	}
	_, err := tx.ExecContext(r.Context(), `INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled)
        VALUES($1,$2,$3,$4,$5) ON CONFLICT(outlet_id) DO UPDATE SET slug=excluded.slug,display_name=excluded.display_name,
        cancellation_policy=excluded.cancellation_policy,enabled=excluded.enabled`, outlet, input.Slug, name, policy, *input.Enabled)
	if err != nil {
		return 0, nil, reservationWriteError(err)
	}
	return reservationSettingsRead(r, tx, outlet, nil)
}
func reservationTableCreate(r *http.Request, tx *sql.Tx, outlet int64, body []byte) (int, any, error) {
	var input reservationTable
	if err := decodeJSON(body, &input); err != nil {
		return 0, nil, err
	}
	label, valid := reservationText(input.Label, 1, 32)
	if !valid || input.Capacity < 1 || input.Capacity > 12 {
		return 0, nil, badInput
	}
	input.Label = label
	_, err := tx.ExecContext(r.Context(), `INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES($1,$2,$3)`, outlet, label, input.Capacity)
	return 201, input, reservationWriteError(err)
}
func reservationSlotCreate(r *http.Request, tx *sql.Tx, outlet int64, body []byte) (int, any, error) {
	var input reservationSlot
	if err := decodeJSON(body, &input); err != nil {
		return 0, nil, err
	}
	start, err := reservationStart(input.StartAt, time.Now())
	if err != nil {
		return 0, nil, err
	}
	input.StartAt = start
	_, err = tx.ExecContext(r.Context(), `INSERT INTO reservation_slots(outlet_id,start_at) VALUES($1,$2)`, outlet, start)
	return 201, input, reservationWriteError(err)
}
func reservationConstraintCode(err error) string {
	var pg interface{ SQLState() string }
	if errors.As(err, &pg) {
		return pg.SQLState()
	}
	return ""
}
func reservationConflictError(err error) bool { return reservationConstraintCode(err) == "23505" }
func reservationWriteError(err error) error {
	switch reservationConstraintCode(err) {
	case "23505":
		return problem{409, "Ese dato de reservas ya existe."}
	case "23503":
		return reservationUnavailable
	default:
		return err
	}
}
func listReservationRequests(r *http.Request, tx *sql.Tx, outlet int64, _ []byte) (int, any, error) {
	dates := r.URL.Query()["date"]
	if len(dates) != 1 {
		return 0, nil, badInput
	}
	day, err := reservationDay(dates[0])
	if err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT r.id,r.guest_name,r.party_size,r.start_at,r.status,coalesce(r.table_label,''),p.policy
        FROM reservation_requests r LEFT JOIN reservation_request_policies p ON p.request_id=r.id
        WHERE r.outlet_id=$1 AND r.start_at>=$2 AND r.start_at<$3 ORDER BY r.start_at,r.created_at,r.id
        LIMIT 501`, outlet, day.UTC().Format(time.RFC3339), day.AddDate(0, 0, 1).UTC().Format(time.RFC3339))
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	result := []reservationEntry{}
	for rows.Next() {
		var row reservationEntry
		if err := rows.Scan(&row.ID, &row.GuestName, &row.PartySize, &row.StartAt, &row.Status, &row.TableLabel, &row.CancellationPolicy); err != nil {
			return 0, nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	truncated := len(result) > 500
	if truncated {
		result = result[:500]
	}
	return 200, struct {
		Requests  []reservationEntry `json:"requests"`
		Truncated bool               `json:"truncated"`
	}{result, truncated}, nil
}
