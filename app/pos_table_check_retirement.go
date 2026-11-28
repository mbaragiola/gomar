package main

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/mbaragiola/gomar/app/internal/tablecheck"
)

var posTableCheckChanged = problem{409, "La cuenta de esta mesa cambió. Volvé a consultarla antes de seguir."}

func (a *app) retirePOSTableCheck(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	if !posSellingRole(m.role) {
		return 0, nil, forbiddenRole
	}
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	number, err := pathID(r, "table")
	if err != nil || number < 1 || number > tablecheck.MaxTable || strconv.FormatInt(number, 10) != r.PathValue("table") || r.URL.RawQuery != "" {
		return 0, nil, badInput
	}
	var input struct {
		ID     string `json:"id_cuenta"`
		Reason string `json:"motivo"`
	}
	if err := decodePayables(raw, &input, "id_cuenta", "motivo"); err != nil {
		return 0, nil, err
	}
	id, err := strconv.ParseInt(input.ID, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != input.ID ||
		(input.Reason != "saved_in_error" && input.Reason != "cancelled") {
		return 0, nil, badInput
	}
	ctx := r.Context()
	// Keep this guard even when the tender preview flag is off.
	var hasActiveTender bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
	SELECT 1 FROM pos_table_check_slots s JOIN pos_table_checks c
	ON c.id=s.check_id AND c.outlet_id=s.outlet_id
	WHERE s.outlet_id=$1 AND s.table_number=$2 AND s.check_id=$3 AND `+posActiveTender+`)`, outlet, number, id).Scan(&hasActiveTender); err != nil {
		return 0, nil, err
	}
	if hasActiveTender {
		return 0, nil, posTableTenderBlocksRetirement
	}
	// Same outlet lock as Save. A stale action must never free a replacement's slot.
	result, err := tx.ExecContext(ctx, `DELETE FROM pos_table_check_slots WHERE outlet_id=$1 AND table_number=$2 AND check_id=$3`, outlet, number, id)
	if err != nil {
		return 0, nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, nil, err
	}
	if n == 1 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pos_table_check_retirements(check_id,outlet_id,table_number,actor_id,reason)
 VALUES($1,$2,$3,$4,$5)`, id, outlet, number, m.userID, input.Reason); err != nil {
			return 0, nil, err
		}
	} else {
		// An identical retry from the same operator confirms only their original retirement.
		var replay bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pos_table_check_retirements
 WHERE check_id=$1 AND outlet_id=$2 AND table_number=$3 AND actor_id=$4 AND reason=$5)`, id, outlet, number, m.userID, input.Reason).Scan(&replay)
		if err != nil {
			return 0, nil, err
		}
		if !replay {
			return 0, nil, posTableCheckChanged
		}
	}
	row, err := loadPOSTableCheck(ctx, tx, outlet, number, id)
	return http.StatusOK, row, err
}
