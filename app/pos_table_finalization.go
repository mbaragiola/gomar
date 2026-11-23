package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/mbaragiola/gomar/app/internal/postender"
	"github.com/mbaragiola/gomar/app/internal/tablecheck"
)

var posTableNotCovered = problem{409, "La cuenta no tiene todos los pagos anotados. Volvé a consultarla."}

type posFinalizationReceipt struct {
	Check string `json:"id_cuenta"`
	Sale  string `json:"id_venta"`
	Total int64  `json:"total_centavos"`
}

func (a *app) finalizePOSTable(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	var in struct {
		Check    string `json:"id_cuenta"`
		Request  string `json:"id_solicitud"`
		Revision string `json:"revision_pagos"`
	}
	if err := decodeWikiProposalJSON(raw, &in, "id_cuenta", "id_solicitud", "revision_pagos"); err != nil {
		return 0, nil, err
	}
	number, check, revision := posCanonicalID(r.PathValue("table")), posCanonicalID(in.Check), posCanonicalID(in.Revision)
	if number < 1 || number > tablecheck.MaxTable || check < 1 || revision < 1 || !posRequestID.MatchString(in.Request) || r.URL.RawQuery != "" {
		return 0, nil, badInput
	}
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	ctx := r.Context()
	var savedID, savedCheck, savedActor, savedTable, savedRevision int64
	receipt := posFinalizationReceipt{Check: in.Check}
	// Only an exact replay from a currently authorized clerk may inspect
	// a historical result. It must not load or clear the replacement slot.
	err = tx.QueryRowContext(ctx, `SELECT s.id,s.actor_id,s.total_minor,f.check_id,f.table_number,f.tender_revision
 FROM pos_sales s JOIN pos_table_finalizations f ON f.sale_id=s.id AND f.outlet_id=s.outlet_id
 WHERE s.request_id=$1 AND s.outlet_id=$2`, in.Request, outlet).
		Scan(&savedID, &savedActor, &receipt.Total, &savedCheck, &savedTable, &savedRevision)
	if err == nil {
		if savedActor != m.userID || savedCheck != check || savedTable != number || savedRevision != revision {
			return 0, nil, posConflict
		}
		receipt.Sale = strconv.FormatInt(savedID, 10)
		return http.StatusOK, receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	t, err := loadPOSTenderTarget(r, tx, m, true)
	if err != nil {
		return 0, nil, err
	}
	if t.scope.CheckID != check {
		return 0, nil, posTableCheckChanged
	}
	j, events, _, err := loadPOSTenderHistory(ctx, tx, t)
	if err != nil {
		return 0, nil, err
	}
	if len(events) == 0 || j.Balance().OutstandingMinor != 0 {
		return 0, nil, posTableNotCovered
	}
	if events[len(events)-1].ID != in.Revision {
		return 0, nil, posTableTenderConflict
	}
	if replay, err := j.Seal(postender.Request{Scope: t.scope, ActorID: m.userID, RequestID: in.Request}); err != nil || replay {
		return 0, nil, posTableTenderConflict
	}
	// A finalize request cannot be reused as a tender command in this outlet.
	var used bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pos_table_tender_events WHERE outlet_id=$1 AND request_id=$2)`, outlet, in.Request).Scan(&used); err != nil {
		return 0, nil, err
	}
	if used {
		return 0, nil, posTableTenderConflict
	}
	voided := map[string]bool{}
	for _, e := range events {
		if e.Kind == "void" {
			voided[e.Target] = true
		}
	}
	method := ""
	for _, e := range events {
		if e.Kind == "record" && !voided[e.ID] {
			if method == "" {
				method = e.Method
			} else if method != e.Method {
				method = "mixto"
			}
		}
	}
	var sale int64
	err = tx.QueryRowContext(ctx, `INSERT INTO pos_sales(request_id,outlet_id,actor_id,total_minor,recorded_at,payment_method)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(request_id) DO NOTHING RETURNING id`, in.Request, outlet, m.userID, t.total, time.Now().UTC().Format(time.RFC3339Nano), method).Scan(&sale)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, posConflict
	}
	if err != nil {
		return 0, nil, err
	}
	lines, err := tx.ExecContext(ctx, `INSERT INTO pos_sale_items(sale_id,outlet_id,product_id,revision,quantity)
 SELECT $1,outlet_id,product_id,revision,quantity FROM pos_table_check_items WHERE outlet_id=$2 AND check_id=$3`, sale, outlet, check)
	if err != nil {
		return 0, nil, err
	}
	if n, err := lines.RowsAffected(); err != nil || n == 0 {
		return 0, nil, fmt.Errorf("could not copy saved check items: %d %v", n, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO pos_table_finalizations(check_id,outlet_id,table_number,sale_id,tender_revision)
 VALUES($1,$2,$3,$4,$5)`, check, outlet, number, sale, revision); err != nil {
		return 0, nil, err
	}
	for _, e := range events {
		if e.Kind == "record" && !voided[e.ID] {
			if _, err = tx.ExecContext(ctx, `INSERT INTO pos_table_finalization_legs(check_id,outlet_id,tender_id) VALUES($1,$2,$3)`, check, outlet, posCanonicalID(e.ID)); err != nil {
				return 0, nil, err
			}
		}
	}
	if a.enableStock {
		if err = recordPOSStockSale(ctx, tx, outlet, sale); err != nil {
			return 0, nil, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM pos_table_check_slots WHERE outlet_id=$1 AND table_number=$2 AND check_id=$3`, outlet, number, check)
	if err != nil {
		return 0, nil, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return 0, nil, fmt.Errorf("could not release exact table slot: %d %v", n, err)
	}
	receipt.Sale, receipt.Total = strconv.FormatInt(sale, 10), t.total
	return http.StatusCreated, receipt, nil
}
