package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"time"

	"github.com/mbaragiola/gomar/app/internal/publicconcierge"
)

const (
	reservationMenuMax  = 4 << 20
	reservationMenuName = "menu.pdf"
	publicMenuPrefix    = "/api/v1/public/reservas/"
	menuStaffPath       = "/api/v1/entities/{entity}/outlets/{outlet}/reservations/menu"
)

var menuVenueSlug = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$`)
var errNoMenu = problem{404, "El menú no está disponible."}

type reservationMenuStatus struct {
	Available bool   `json:"disponible"`
	Filename  string `json:"nombre,omitempty"`
	Bytes     int    `json:"tamano,omitempty"`
	Updated   string `json:"actualizado_en,omitempty"`
}

// registerReservationMenus attaches the same explicit public reservation gate. The public service has no private file access.
func (a *app) registerReservationMenus(add func(string, string, http.HandlerFunc), publicEnabled bool) {
	for _, method := range []string{"GET", "HEAD"} {
		add(publicMenuPrefix+"{venue}/menu.pdf", method, func(w http.ResponseWriter, r *http.Request) {
			if !publicEnabled {
				w.Header().Set("Cache-Control", "no-store")
				failure(w, errNoMenu)
				return
			}
			a.publicReservationMenu(w, r)
		})
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		add(menuStaffPath, method, a.protected(a.staffReservationMenu))
	}
}

// validReservationPDF screens file type and size, not PDF contents. It does
// not claim to sanitize active content. The public response is attachment-only.
func validReservationPDF(payload []byte) bool {
	if len(payload) < 16 || len(payload) > reservationMenuMax {
		return false
	}
	data := bytes.TrimRight(payload, " \r\n\t\f")
	return len(data) >= 16 && (bytes.HasPrefix(data, []byte("%PDF-1.")) || bytes.HasPrefix(data, []byte("%PDF-2."))) &&
		len(data) > 8 && data[7] >= '0' && data[7] <= '9' && (data[8] == '\r' || data[8] == '\n') &&
		bytes.HasSuffix(data, []byte("%%EOF"))
}

func (a *app) staffReservationMenu(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	outlet, err := pathID(r, "outlet")
	if err != nil {
		return err
	}
	// Do not hold the SQLite writer lock while reading a slow request body.
	var body []byte
	var bodyErr error
	if r.Method == "PUT" {
		kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || kind != "application/pdf" || len(params) != 0 {
			bodyErr = problem{415, "Subí el menú en formato PDF."}
		} else {
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, reservationMenuMax))
			var tooLarge *http.MaxBytesError
			switch {
			case errors.As(err, &tooLarge):
				bodyErr = problem{413, "El menú no puede pesar más de 4 MiB."}
			case err != nil || !validReservationPDF(body):
				bodyErr = problem{422, "No se pudo leer el PDF del menú."}
			}
		}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := scopedMembership(tx, u.id, entity, "manage_entity_access")
	if err != nil {
		return err
	}
	// Recheck active user after reading the body, within the write transaction.
	if err := requireReservationMenuStaff(r.Context(), tx, u.id, m, entity, outlet); err != nil {
		return err
	}
	if !safeMethod(r.Method) {
		if err := lockPostgresOutlet(r.Context(), tx, outlet); err != nil {
			return err
		}
		fresh, err := scopedMembership(tx, u.id, entity, "manage_entity_access")
		if err != nil {
			return err
		}
		if err := requireReservationMenuStaff(r.Context(), tx, u.id, fresh, entity, outlet); err != nil {
			return err
		}
	}
	if bodyErr != nil {
		return bodyErr
	}
	switch r.Method {
	case "PUT":
		_, err = tx.ExecContext(r.Context(), `INSERT INTO reservation_menus(outlet_id,body,updated_at,updated_by) VALUES($1,$2,$3,$4)
    ON CONFLICT(outlet_id) DO UPDATE SET body=excluded.body,updated_at=excluded.updated_at,updated_by=excluded.updated_by`,
			outlet, body, time.Now().UTC().Format(time.RFC3339Nano), u.id)
	case "DELETE":
		_, err = tx.ExecContext(r.Context(), `DELETE FROM reservation_menus WHERE outlet_id=$1`, outlet)
	}
	if err != nil {
		return err
	}
	status, err := readReservationMenuStatus(r.Context(), tx, outlet)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, 200, status)
	return nil
}

func requireReservationMenuStaff(ctx context.Context, tx *sql.Tx, userID int64, m entityMembership, entity, outlet int64) error {
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
		JOIN reservation_venues v ON v.outlet_id=o.id JOIN users u ON u.id=$1
		WHERE g.membership_id=$2 AND o.entity_id=$3 AND o.id=$4 AND o.active AND u.active
	)`, userID, m.id, entity, outlet).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return missingOutlet
	}
	return nil
}

func readReservationMenuStatus(ctx context.Context, tx *sql.Tx, outlet int64) (reservationMenuStatus, error) {
	var out reservationMenuStatus
	err := tx.QueryRowContext(ctx, `SELECT length(body),updated_at FROM reservation_menus WHERE outlet_id=$1`, outlet).Scan(&out.Bytes, &out.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Available = true
	out.Filename = reservationMenuName
	return out, nil
}

func (a *app) publicReservationMenu(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	slug := r.PathValue("venue")
	if !menuVenueSlug.MatchString(slug) {
		failure(w, errNoMenu)
		return
	}
	var body []byte
	// No filesystem paths, no private-doc traversal, and only enabled, active venues.
	err := a.db.QueryRowContext(r.Context(), `SELECT m.body FROM reservation_menus m
  JOIN reservation_venues v ON v.outlet_id=m.outlet_id JOIN outlets o ON o.id=v.outlet_id
  JOIN entities e ON e.id=o.entity_id WHERE v.slug=$1 AND v.enabled AND o.active AND e.active
  AND length(m.body) BETWEEN 16 AND 4194304`, slug).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		failure(w, errNoMenu)
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="menu.pdf"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, reservationMenuName, time.Time{}, bytes.NewReader(body))
}

// reservationMenuPath should only be sent as metadata when this outlet has
// a deliberately published menu and public reservations are enabled.
func reservationMenuPath(slug string) string {
	if !menuVenueSlug.MatchString(slug) {
		return ""
	}
	return publicMenuPrefix + slug + "/menu.pdf"
}

// reservationMenuForConcierge reports fixed same-origin PDF metadata only
// for an enabled venue where staff explicitly uploaded the public menu.
func reservationMenuForConcierge(ctx context.Context, q sqlQueryRower, slug string, publicEnabled bool) (*publicconcierge.MenuPDF, error) {
	path := reservationMenuPath(slug)
	if path == "" || !publicEnabled {
		return nil, nil
	}
	var present bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM reservation_menus m JOIN reservation_venues v ON v.outlet_id=m.outlet_id
  JOIN outlets o ON o.id=v.outlet_id JOIN entities e ON e.id=o.entity_id
  WHERE v.slug=$1 AND v.enabled AND o.active AND e.active
 )`, slug).Scan(&present)
	if err != nil || !present {
		return nil, err
	}
	return &publicconcierge.MenuPDF{Path: path, Filename: reservationMenuName}, nil
}

type sqlQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
