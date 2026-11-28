package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mbaragiola/gomar/app/internal/postime"
)

const tenantSalesExportLimit = 1000
const tenantSalesExportCapability = "export_financial_reports"

var tenantSalesExportConflict = problem{409, "La referencia de exportación ya corresponde a otra solicitud."}
var missingTenantSalesExport = problem{404, "No se encontró la exportación."}

type tenantSalesExportScope struct {
	entity, outlet int64
	requestID      string
}

type tenantSalesExportResult struct {
	CSV          []byte
	Digest       string
	Acknowledged bool
}

func (a *app) beginTenantSalesExport(r *http.Request, u user, scope tenantSalesExportScope) (*sql.Tx, error) {
	if scope.entity < 1 || scope.outlet < 1 || !postime.ValidRequestID(scope.requestID) {
		return nil, badInput
	}
	if r.Method != http.MethodPost || !validCSRF(r) {
		return nil, badCSRF
	}
	tx, err := a.beginEmployeeCheckedWriteWithOptions(r, u, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	member, err := scopedMembership(tx, u.id, scope.entity, tenantSalesExportCapability)
	if err == nil {
		err = requireSelectedDocumentOutlet(tx, member, scope.outlet)
	}
	if err == nil {
		member, err = a.lockAccountingMembership(r, tx, scope.entity, u.id, tenantSalesExportCapability)
	}
	if err == nil {
		err = a.requireTenantSalesExportOutlet(r, tx, member, scope.outlet)
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (a *app) requireTenantSalesExportOutlet(r *http.Request, tx *sql.Tx, member entityMembership, outlet int64) error {
	if err := requireSelectedDocumentOutlet(tx, member, outlet); err != nil {
		return err
	}
	if a.hostedDiscoveryRead != nil {
		scopes, err := a.hostedDiscoveryRead.discover(r.Context(), tx, member.userID,
			sql.NullInt64{Int64: member.ID, Valid: true}, sql.NullInt64{Int64: outlet, Valid: true}, false)
		if err != nil {
			return err
		}
		if len(scopes[member.ID]) != 1 {
			return missingOutlet
		}
	}
	return nil
}

// An exact retry regenerates the same bytes from immutable
// sale rows. Preparing the export makes no assertion about delivery.
func (a *app) prepareTenantSalesExport(r *http.Request, u user, scope tenantSalesExportScope, fromDay, throughDay string) (tenantSalesExportResult, error) {
	var result tenantSalesExportResult
	from, fromErr := reservationDay(fromDay)
	through, throughErr := reservationDay(throughDay)
	if fromErr != nil || throughErr != nil || through.Before(from) || !through.Before(from.AddDate(0, 0, 31)) {
		return result, badInput
	}
	tx, err := a.beginTenantSalesExport(r, u, scope)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	ctx := r.Context()
	var rawIDs, recordedDigest []byte
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT entity_id=$3 AND outlet_id=$4 AND from_day=$5::date AND through_day=$6::date,sale_ids,digest,
		EXISTS(SELECT 1 FROM gomar.tenant_sales_export_acknowledgments a WHERE (a.actor_id,a.request_id)=(e.actor_id,e.request_id))
		FROM gomar.tenant_sales_exports e WHERE actor_id=$1 AND request_id=$2`, u.id, scope.requestID,
		scope.entity, scope.outlet, fromDay, throughDay).Scan(&exact, &rawIDs, &recordedDigest, &result.Acknowledged)
	first := errors.Is(err, sql.ErrNoRows)
	if err != nil && !first {
		return tenantSalesExportResult{}, err
	}
	if !first && !exact {
		return tenantSalesExportResult{}, tenantSalesExportConflict
	}

	query := `SELECT id,recorded_at,total_minor FROM gomar.pos_sales
		WHERE outlet_id=$1 AND recorded_at >= $2 AND recorded_at < $3 ORDER BY recorded_at,id LIMIT $4`
	// A whole-second prefix handles both existing UTC timestamp formats.
	const prefix = "2006-01-02T15:04:05"
	args := []any{scope.outlet, from.UTC().Format(prefix), through.AddDate(0, 0, 1).UTC().Format(prefix), tenantSalesExportLimit + 1}
	var savedIDs []int64
	if !first {
		if err := json.Unmarshal(rawIDs, &savedIDs); err != nil {
			return tenantSalesExportResult{}, err
		}
		query = `SELECT id,recorded_at,total_minor FROM gomar.pos_sales WHERE outlet_id=$1 AND id=ANY($2::bigint[]) ORDER BY recorded_at,id`
		args = []any{scope.outlet, savedIDs}
	}
	ids, data, err := tenantSalesCSV(ctx, tx, query, args...)
	if err != nil {
		return tenantSalesExportResult{}, err
	}
	digest := sha256.Sum256(data)
	if first {
		rawIDs, err := json.Marshal(ids)
		if err != nil {
			return tenantSalesExportResult{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO gomar.tenant_sales_exports
			(actor_id,request_id,entity_id,outlet_id,from_day,through_day,sale_ids,digest)
			VALUES($1,$2,$3,$4,$5::date,$6::date,$7::jsonb,$8) ON CONFLICT (actor_id,request_id) DO NOTHING`,
			u.id, scope.requestID, scope.entity, scope.outlet, fromDay, throughDay, rawIDs, digest[:])
		if err != nil {
			return tenantSalesExportResult{}, err
		}
		// Two different scoped transactions may use the same actor/ref.
		var storedEqual bool
		err = tx.QueryRowContext(ctx, `SELECT entity_id=$3 AND outlet_id=$4 AND from_day=$5::date AND through_day=$6::date AND digest=$7
			FROM gomar.tenant_sales_exports WHERE actor_id=$1 AND request_id=$2`, u.id, scope.requestID,
			scope.entity, scope.outlet, fromDay, throughDay, digest[:]).Scan(&storedEqual)
		if err != nil {
			return tenantSalesExportResult{}, err
		}
		if !storedEqual {
			return tenantSalesExportResult{}, tenantSalesExportConflict
		}
	} else if len(ids) != len(savedIDs) || !bytes.Equal(digest[:], recordedDigest) {
		return tenantSalesExportResult{}, tenantSalesExportConflict
	}
	if err := tx.Commit(); err != nil {
		return tenantSalesExportResult{}, err
	}
	result.CSV, result.Digest = data, hex.EncodeToString(digest[:])
	return result, nil
}

func tenantSalesCSV(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, []byte, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	_ = writer.Write([]string{"registrado_utc", "importe_centavos_ars"})
	ids := []int64{}
	for rows.Next() {
		if len(ids) == tenantSalesExportLimit {
			return nil, nil, problem{422, "Elegí un rango más corto para exportar hasta 1000 ventas."}
		}
		var id, total int64
		var raw string
		if err := rows.Scan(&id, &raw, &total); err != nil {
			return nil, nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, nil, err
		}
		_ = writer.Write([]string{at.UTC().Format(time.RFC3339Nano), strconv.FormatInt(total, 10)})
		ids = append(ids, id)
	}
	writer.Flush()
	return ids, output.Bytes(), errors.Join(rows.Err(), writer.Error())
}

// Record an explicit acknowledgment from the same currently
// authorized requester for exactly the generated bytes.
func (a *app) acknowledgeTenantSalesExport(r *http.Request, u user, scope tenantSalesExportScope, digest string) error {
	if len(digest) != sha256.Size*2 {
		return badInput
	}
	raw, err := hex.DecodeString(digest)
	if err != nil || digest != hex.EncodeToString(raw) {
		return badInput
	}
	tx, err := a.beginTenantSalesExport(r, u, scope)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ctx := r.Context()
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT digest=$5 FROM gomar.tenant_sales_exports
		WHERE actor_id=$1 AND request_id=$2 AND entity_id=$3 AND outlet_id=$4`,
		u.id, scope.requestID, scope.entity, scope.outlet, raw).Scan(&exact)
	if errors.Is(err, sql.ErrNoRows) {
		return missingTenantSalesExport
	}
	if err != nil {
		return err
	}
	if !exact {
		return tenantSalesExportConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO gomar.tenant_sales_export_acknowledgments
		(actor_id,request_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, u.id, scope.requestID); err != nil {
		return err
	}
	return tx.Commit()
}
