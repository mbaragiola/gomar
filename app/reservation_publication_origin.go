package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const reservationPublicationOriginPrefix = "/_turnate/reservations/"

var reservationPublicationChanged = problem{http.StatusPreconditionFailed, "La publicación cambió. Volvé a elegir el horario."}

type reservationPublicationRevision struct {
	business, branch string
	revision         int64
	bookingKey       string
}

func (r reservationPublicationRevision) check(ctx context.Context, tx *sql.Tx, scope reservationPublication, lock bool) error {
	query := `SELECT revision FROM reservation_public_branches
		WHERE business_slug=$1 AND slug=$2 AND entity_id=$3 AND outlet_id=$4 AND venue=$5 AND enabled`
	if lock {
		query += " FOR SHARE"
	}
	var current int64
	err := tx.QueryRowContext(ctx, query,
		r.business, r.branch, scope.entity, scope.outlet, scope.venue).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) || err == nil && current != r.revision {
		return reservationPublicationChanged
	}
	return err
}

func (r reservationPublicationRevision) bookingReplay(ctx context.Context, tx *sql.Tx, outlet int64,
	in reservationPublicInput, start, name string) (reservationResult, [32]byte, error) {
	in.StartAt, in.GuestName = start, name
	raw, err := json.Marshal(in)
	if err != nil {
		return reservationResult{}, [32]byte{}, err
	}
	digest := sha256.Sum256(raw)
	var old []byte
	var id string
	err = tx.QueryRowContext(ctx, `SELECT payload_digest,reservation_id FROM reservation_public_booking_keys
		WHERE outlet_id=$1 AND booking_key=$2`, outlet, r.bookingKey).Scan(&old, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return reservationResult{}, digest, nil
	}
	if err != nil {
		return reservationResult{}, digest, err
	}
	if !bytes.Equal(old, digest[:]) {
		return reservationResult{}, digest, reservationConflict
	}
	policy, err := reservationSubmittedPolicy(ctx, tx, id)
	return reservationResult{ID: id, Status: "pending", CancellationPolicy: policy}, digest, err
}

type publishedReservationBranch struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Revision    string `json:"revision"`
	Policy      string `json:"cancellation_policy,omitempty"`
	scope       reservationPublication
	revision    int64
}

type publishedReservationBusiness struct {
	DisplayName string                       `json:"display_name"`
	Branches    []publishedReservationBranch `json:"branches"`
}

func loadPublishedReservationBusiness(ctx context.Context, db *sql.DB, business string) (publishedReservationBusiness, error) {
	var result publishedReservationBusiness
	rows, err := db.QueryContext(ctx, `SELECT b.display_name, p.slug, v.display_name, p.revision, v.cancellation_policy,
		p.entity_id, p.outlet_id, p.venue
		FROM reservation_public_businesses b
		JOIN entities e ON e.id=b.entity_id AND e.active
		JOIN reservation_public_branches p ON p.business_slug=b.slug AND p.enabled
		JOIN outlets o ON o.id=p.outlet_id AND o.entity_id=p.entity_id AND o.active
		JOIN reservation_venues v ON v.outlet_id=p.outlet_id AND v.slug=p.venue AND v.enabled
		WHERE b.slug=$1 ORDER BY p.slug`, business)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var branch publishedReservationBranch
		if err := rows.Scan(&result.DisplayName, &branch.Slug, &branch.DisplayName, &branch.revision, &branch.Policy,
			&branch.scope.entity, &branch.scope.outlet, &branch.scope.venue); err != nil {
			return result, err
		}
		branch.Revision = strconv.FormatInt(branch.revision, 10)
		result.Branches = append(result.Branches, branch)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Branches) == 0 {
		return result, reservationUnavailable
	}
	return result, nil
}

func validReservationPublicationSecret(secret string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	return err == nil && len(decoded) == 32
}

func withReservationPublicationOrigin(ordinary, guest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, reservationPublicationOriginPrefix) {
			guest.ServeHTTP(w, r)
			return
		}
		ordinary.ServeHTTP(w, r)
	})
}

// The publication origin is only available to an authenticated HTTPS peer.
func reservationPublicationOrigin(db *sql.DB, credential string, clock func() time.Time) (http.Handler, error) {
	if db == nil || clock == nil || !validReservationPublicationSecret(credential) {
		return nil, errors.New("invalid reservation publication origin configuration")
	}
	correct := sha256.Sum256([]byte("Bearer " + credential))
	var active atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		received := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if !effectiveHTTPS(r) || len(r.Header.Values("Authorization")) != 1 ||
			subtle.ConstantTimeCompare(received[:], correct[:]) != 1 {
			failure(w, reservationUnavailable)
			return
		}
		path, ok := strings.CutPrefix(r.URL.Path, reservationPublicationOriginPrefix)
		parts := strings.Split(path, "/")
		if !ok || r.URL.RawPath != "" || len(parts) > 3 || !reservationValidSlug(parts[0]) ||
			len(parts) >= 2 && !reservationValidSlug(parts[1]) {
			failure(w, reservationUnavailable)
			return
		}
		action := ""
		if len(parts) == 3 {
			action = parts[2]
			if action != "availability" && action != "requests" {
				failure(w, reservationUnavailable)
				return
			}
		}
		method := http.MethodGet
		if action == "requests" {
			method = http.MethodPost
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		release, err := admitRequestWork(w, &active, publicReservationConcurrency)
		if err != nil {
			failure(w, err)
			return
		}
		defer release()
		code, result, err := serveReservationPublication(w, r, db, parts, action, clock)
		if err != nil {
			failure(w, err)
			return
		}
		jsonResponse(w, code, result)
	}), nil
}

func serveReservationPublication(w http.ResponseWriter, r *http.Request, db *sql.DB,
	parts []string, action string, clock func() time.Time) (int, any, error) {
	ctx := r.Context()
	if action != "availability" && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		return 0, nil, badInput
	}
	business, err := loadPublishedReservationBusiness(ctx, db, parts[0])
	if err != nil {
		return 0, nil, err
	}
	if len(parts) == 1 {
		return http.StatusOK, business, nil
	}
	for _, branch := range business.Branches {
		if branch.Slug != parts[1] {
			continue
		}
		if action == "" {
			return http.StatusOK, branch, nil
		}
		expected := `"` + branch.Revision + `"`
		if len(r.Header.Values("If-Match")) != 1 || r.Header.Get("If-Match") != expected {
			return 0, nil, reservationPublicationChanged
		}
		svc, err := publishedReservations(db, branch.scope)
		if err != nil {
			return 0, nil, err
		}
		svc.publishedRevision = &reservationPublicationRevision{business: parts[0], branch: branch.Slug, revision: branch.revision}
		if action == "availability" {
			query, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil {
				return 0, nil, badInput
			}
			party, err := strconv.Atoi(query.Get("party_size"))
			if err != nil || len(query) != 2 || len(query["date"]) != 1 || len(query["party_size"]) != 1 {
				return 0, nil, badInput
			}
			slots, err := svc.availability(ctx, branch.scope.venue, query.Get("date"), party, clock())
			return http.StatusOK, struct {
				Slots []reservationSlot `json:"slots"`
			}{slots}, err
		}
		key := r.Header.Get("Idempotency-Key")
		if len(r.Header.Values("Idempotency-Key")) != 1 || !reservationID.MatchString(key) {
			return 0, nil, badInput
		}
		svc.publishedRevision.bookingKey = key
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			return 0, nil, problem{415, "Enviá los datos en formato JSON."}
		}
		var in reservationPublicInput
		if err := parseJSON(w, r, &in); err != nil {
			return 0, nil, err
		}
		result, err := svc.requestPublic(ctx, branch.scope.venue, in, clock(), false)
		return http.StatusCreated, result, err
	}
	return 0, nil, reservationUnavailable
}
