package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/mbaragiola/gomar/app/internal/posbarcode"
)

func (a *app) registerMCPBookingReads(add func(string, string, http.HandlerFunc)) {
	add(mcpWorkspaceBase+"/reservation-status", http.MethodGet, a.mcpRead(mcpReservationStatus))
	add(mcpWorkspaceBase+"/reservation-setup", http.MethodGet, a.mcpRead(a.mcpReservationSetup))
	add(mcpWorkspaceBase+"/reservation-start-setup", http.MethodGet, a.mcpRead(a.mcpReservationStartSetup))
	add(mcpWorkspaceBase+"/reservation-table-setup", http.MethodGet, a.mcpRead(mcpReservationTableSetup))
	add(mcpWorkspaceBase+"/published-menu", http.MethodGet, a.mcpRead(a.mcpPublishedMenu))
	if a.enablePOS {
		add(mcpWorkspaceBase+"/pos-catalog", http.MethodGet, a.mcpRead(mcpPOSCatalog))
		add(mcpWorkspaceBase+"/pos-catalog/{product}", http.MethodGet, a.mcpRead(mcpPOSCatalogProduct))
		if a.enableStock {
			add(mcpWorkspaceBase+"/stock/products/{product}/movements-summary", http.MethodGet, a.mcpRead(a.mcpStockMovements))
		}
	}
}

const utcSecondPrefix = "2006-01-02T15:04:05"

func (a *app) mcpReservationSetup(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	if err = requireReservationStaff(r.Context(), tx, s.entityMembership, outlet); err != nil {
		return nil, err
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, badInput
	}
	saved := false
	result := mcpclient.ReservationSetup{Timezone: reservationTimezone, PublicFeatureEnabled: a.publicReservations, MenuSaved: &saved}
	// Private preparation status only. Do not read the menu body or metadata,
	// and do not make its existence depend on the publication flags.
	err = tx.QueryRowContext(r.Context(), `SELECT v.enabled, EXISTS(
		SELECT 1 FROM reservation_menus m WHERE m.outlet_id=v.outlet_id
	) FROM reservation_venues v WHERE v.outlet_id=$1`, outlet).Scan(&result.VenueEnabled, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	result.VenueConfigured = true
	return result, nil
}

func (a *app) mcpReservationStartSetup(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	if err := requireReservationStaff(ctx, tx, s.entityMembership, outlet); err != nil {
		return nil, err
	}
	raw, err := mcpOptionalQuery(r, "start_at")
	if err != nil {
		return nil, err
	}
	if _, err := parseReservationSlotAddStart(raw); err != nil {
		return nil, err
	}
	start, err := reservationStart(raw, a.tokenTime())
	if err != nil {
		return nil, err
	}
	// Check configuration only: no venue enable/public flag, tables, requests
	// or pending proposals are consulted here.
	var result mcpclient.ReservationStartSetup
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM reservation_slots s WHERE s.outlet_id=v.outlet_id AND s.start_at=$2
	) FROM reservation_venues v WHERE v.outlet_id=$1`, outlet, start).Scan(&result.StartConfigured)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, reservationUnavailable
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func mcpReservationTableSetup(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	if err := requireReservationStaff(ctx, tx, s.entityMembership, outlet); err != nil {
		return nil, err
	}
	raw, err := mcpOptionalQuery(r, "table_number")
	if err != nil {
		return nil, err
	}
	number, err := strconv.Atoi(raw)
	if err != nil || !validReservationTableNumber(number) || strconv.Itoa(number) != raw {
		return nil, badInput
	}
	// Free-form native table labels can have multiple aliases for this number.
	// Report existence but never choose a capacity when the match is ambiguous.
	var result mcpclient.ReservationTableSetup
	err = tx.QueryRowContext(ctx, `SELECT matched.configured,matched.capacity
	FROM reservation_venues v CROSS JOIN LATERAL (
		SELECT count(*)>0 AS configured, CASE WHEN count(*)=1 THEN min(t.capacity) END AS capacity
		FROM reservation_tables t WHERE t.outlet_id=v.outlet_id AND lower(t.label) ~ $2
	) matched WHERE v.outlet_id=$1`, outlet, reservationTableNumberPattern(number)).Scan(&result.TableConfigured, &result.Capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, reservationUnavailable
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func mcpReservationStatus(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	if err := requireReservationStaff(r.Context(), tx, s.entityMembership, outlet); err != nil {
		return nil, err
	}
	raw, err := mcpOptionalQuery(r, "date")
	if err != nil {
		return nil, err
	}
	day, err := reservationDay(raw)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT status,count(*),CAST(SUM(party_size) AS BIGINT)
 FROM reservation_requests WHERE outlet_id=$1 AND start_at >= $2 AND start_at < $3
 GROUP BY status`, outlet, day.UTC().Format(utcSecondPrefix), day.AddDate(0, 0, 1).UTC().Format(utcSecondPrefix))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := mcpclient.ReservationStatus{Date: raw, Timezone: reservationTimezone, Counts: []mcpclient.ReservationCount{
		{Status: "pending"}, {Status: "confirmed"}, {Status: "cancelled"},
	}}
	for rows.Next() {
		var state string
		var count, guests int64
		if err := rows.Scan(&state, &count, &guests); err != nil {
			return nil, err
		}
		index := slices.IndexFunc(result.Counts, func(x mcpclient.ReservationCount) bool { return x.Status == state })
		if index < 0 {
			return nil, errors.New("unexpected reservation status")
		}
		result.Counts[index] = mcpclient.ReservationCount{Status: state, RequestCount: count, PartySizeTotal: guests}
	}
	return result, rows.Err()
}

func (a *app) mcpPublishedMenu(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(capabilities[s.role], "manage_entity_access") {
		return nil, forbiddenRole
	}
	// Staff permissions and publication are different: a saved but hidden
	// menu does not expose file details to the MCP host.
	if !a.publicReservations {
		return mcpclient.PublishedMenu{}, nil
	}
	var enabled bool
	var slug string
	err = tx.QueryRowContext(r.Context(), `SELECT slug,enabled FROM reservation_venues WHERE outlet_id=$1`, outlet).Scan(&slug, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return mcpclient.PublishedMenu{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !enabled || !menuVenueSlug.MatchString(slug) {
		return mcpclient.PublishedMenu{}, nil
	}
	if err := requireReservationMenuStaff(r.Context(), tx, s.userID, s.entityMembership, s.ID, outlet); err != nil {
		return nil, err
	}
	status, err := readReservationMenuStatus(r.Context(), tx, outlet)
	if err != nil {
		return nil, err
	}
	return mcpclient.PublishedMenu{Published: status.Available, Filename: status.Filename, SizeBytes: status.Bytes, UpdatedAt: status.Updated}, nil
}

func mcpPOSCatalog(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) > 1 {
		return nil, badInput
	}
	var after int64
	var barcode string
	if len(q) == 1 {
		switch {
		case len(q["after_product_id"]) == 1:
			cursor := q.Get("after_product_id")
			if cursor != "" {
				after, err = strconv.ParseInt(cursor, 10, 64)
				if err != nil || after <= 0 {
					return nil, badInput
				}
			}
		case len(q["barcode"]) == 1 && posbarcode.Valid(q.Get("barcode")):
			barcode = q.Get("barcode")
		default:
			return nil, badInput
		}
	}
	var rows *sql.Rows
	if barcode != "" {
		rows, err = tx.QueryContext(r.Context(), currentOutletPOSProducts+` AND p.barcode=$2`, outlet, barcode)
	} else {
		rows, err = tx.QueryContext(r.Context(), currentOutletPOSProducts+` AND p.id>$2 ORDER BY p.id LIMIT $3`, outlet, after, mcpPageSize+1)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := mcpclient.POSCatalog{Currency: "ARS", Products: []mcpclient.POSProduct{}}
	for rows.Next() {
		var item mcpclient.POSProduct
		if err := rows.Scan(&item.ID, &item.Name, &item.PriceMinor, &item.Revision, &item.Active, &item.Barcode); err != nil {
			return nil, err
		}
		result.Products = append(result.Products, item)
	}
	if len(result.Products) > mcpPageSize {
		result.Products = result.Products[:mcpPageSize]
		result.NextProductID = result.Products[len(result.Products)-1].ID
	}
	return result, rows.Err()
}

func (a *app) mcpStockMovements(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if !stockReadRole(s.role) {
		return nil, forbiddenRole
	}
	outlet, err := a.mcpStockReadOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	product, err := pathID(r, "product")
	if err != nil || product <= 0 {
		return nil, badInput
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["start_date"]) != 1 || len(q["days"]) != 1 {
		return nil, badInput
	}
	start, err := reservationDay(q.Get("start_date"))
	if err != nil {
		return nil, err
	}
	days, err := strconv.Atoi(q.Get("days"))
	if err != nil || days < 1 || days > 31 {
		return nil, badInput
	}
	end := start.AddDate(0, 0, days)
	var tracked bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM stock_products WHERE outlet_id=$1 AND product_id=$2)`, outlet, product).Scan(&tracked); err != nil {
		return nil, err
	}
	if !tracked {
		return nil, posMissingProduct
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT kind,count(*),CAST(SUM(delta) AS BIGINT)
 FROM stock_movements WHERE outlet_id=$1 AND product_id=$2 AND recorded_at >= $3 AND recorded_at < $4 GROUP BY kind`,
		outlet, product, start.UTC().Format(utcSecondPrefix), end.UTC().Format(utcSecondPrefix))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := mcpclient.StockMovements{ProductID: product, StartDate: start.Format(time.DateOnly), EndDateExclusive: end.Format(time.DateOnly),
		Timezone: reservationTimezone, Kinds: []mcpclient.MovementKind{{Kind: "recepcion"}, {Kind: "merma"}, {Kind: "conteo"}, {Kind: "venta"}}}
	for rows.Next() {
		var item mcpclient.MovementKind
		if err := rows.Scan(&item.Kind, &item.MovementCount, &item.NetUnits); err != nil {
			return nil, err
		}
		index := slices.IndexFunc(result.Kinds, func(x mcpclient.MovementKind) bool { return x.Kind == item.Kind })
		if index < 0 {
			return nil, errors.New("unexpected stock movement kind")
		}
		result.Kinds[index] = item
	}
	return result, rows.Err()
}
