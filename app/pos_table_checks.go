package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/mbaragiola/gomar/app/internal/tablecheck"
)

var (
	posTableCheckMissing  = problem{404, "No hay una cuenta guardada para esa mesa."}
	posTableCheckOccupied = problem{409, "Esta mesa ya tiene una cuenta guardada. Consultala antes de seguir."}
)

type posTableCheck struct {
	ID    string        `json:"id_cuenta"`
	State string        `json:"estado"`
	Table int64         `json:"mesa"`
	Total int64         `json:"total_centavos"`
	Items []posSaleItem `json:"items"`
}

func posSellingRole(role string) bool {
	switch role {
	case "administration", "purchasing", "accounting":
		return true
	default:
		return false
	}
}

func (a *app) readPOSTableCheck(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
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
	var id int64
	err = tx.QueryRowContext(r.Context(), `SELECT check_id FROM pos_table_check_slots WHERE outlet_id=$1 AND table_number=$2`, outlet, number).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, posTableCheckMissing
	}
	if err != nil {
		return 0, nil, err
	}
	row, err := loadPOSTableCheck(r.Context(), tx, outlet, number, id)
	return http.StatusOK, row, err
}

func loadPOSTableCheck(ctx context.Context, tx *sql.Tx, outlet, number, id int64) (posTableCheck, error) {
	row := posTableCheck{ID: strconv.FormatInt(id, 10), State: "sin_cobro", Table: number, Items: []posSaleItem{}}
	var retired bool
	err := tx.QueryRowContext(ctx, `SELECT c.total_minor,r.check_id IS NOT NULL
 FROM pos_table_checks c LEFT JOIN pos_table_check_retirements r ON r.check_id=c.id
 WHERE c.outlet_id=$1 AND c.table_number=$2 AND c.id=$3`, outlet, number, id).Scan(&row.Total, &retired)
	if err != nil {
		return posTableCheck{}, err
	}
	if retired {
		row.State = "retirada"
	}
	items, err := tx.QueryContext(ctx, `SELECT i.product_id,i.revision,i.quantity,v.name,v.price_minor
 FROM pos_table_check_items i JOIN pos_product_versions v
 ON v.product_id=i.product_id AND v.outlet_id=i.outlet_id AND v.revision=i.revision
 WHERE i.outlet_id=$1 AND i.check_id=$2 ORDER BY i.product_id`, outlet, id)
	if err != nil {
		return posTableCheck{}, err
	}
	defer items.Close()
	for items.Next() {
		var line posSaleItem
		if err := items.Scan(&line.ProductID, &line.Revision, &line.Quantity, &line.Name, &line.Price); err != nil {
			return posTableCheck{}, err
		}
		row.Items = append(row.Items, line)
	}
	return row, items.Err()
}

func (a *app) savePOSTableCheck(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	if !posSellingRole(m.role) {
		return 0, nil, forbiddenRole
	}
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	if r.URL.RawQuery != "" {
		return 0, nil, badInput
	}
	var input struct {
		RequestID string    `json:"id_solicitud"`
		Table     int64     `json:"mesa"`
		Items     []posLine `json:"items"`
	}
	if err := decodePayables(raw, &input, "id_solicitud", "mesa", "items"); err != nil {
		return 0, nil, err
	}
	items := make([]tablecheck.Item, 0, len(input.Items))
	for _, line := range input.Items {
		items = append(items, tablecheck.Item{ProductID: line.ProductID, Revision: line.Revision, Quantity: line.Quantity})
	}
	i, err := tablecheck.NewIntent(input.RequestID, input.Table, items)
	if err != nil {
		return 0, nil, badInput
	}
	ctx := r.Context()
	fp := i.Fingerprint()
	var priorID, priorNumber int64
	var priorFP []byte
	err = tx.QueryRowContext(ctx, `SELECT id,table_number,request_fingerprint FROM pos_table_checks
 WHERE outlet_id=$1 AND actor_id=$2 AND request_id=$3`, outlet, m.userID, i.ID()).Scan(&priorID, &priorNumber, &priorFP)
	if err == nil {
		if priorNumber != i.Table() || !bytes.Equal(priorFP, fp[:]) {
			return 0, nil, posConflict
		}
		result, reread := loadPOSTableCheck(ctx, tx, outlet, priorNumber, priorID)
		return http.StatusOK, result, reread
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pos_table_check_slots WHERE outlet_id=$1 AND table_number=$2)`,
		outlet, i.Table()).Scan(&exists); err != nil {
		return 0, nil, err
	}
	if exists {
		return 0, nil, posTableCheckOccupied
	}
	ids := make([]int64, 0, len(items))
	for _, line := range i.Items() {
		ids = append(ids, line.ProductID)
	}
	catalog, err := readPOSTableCurrentCatalog(ctx, tx, outlet, ids)
	if err != nil {
		return 0, nil, err
	}
	lines, total, err := tablecheck.Snapshot(i, outlet, catalog)
	if err != nil {
		return 0, nil, posChangedCatalog
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO pos_table_checks(outlet_id,table_number,actor_id,request_id,request_fingerprint,total_minor)
 VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		outlet, i.Table(), m.userID, i.ID(), fp[:], total).Scan(&id)
	if err != nil {
		return 0, nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pos_table_check_slots(check_id,outlet_id,table_number)
 VALUES($1,$2,$3)`, id, outlet, i.Table()); err != nil {
		return 0, nil, err
	}
	result := posTableCheck{ID: strconv.FormatInt(id, 10), State: "sin_cobro", Table: i.Table(), Total: total, Items: make([]posSaleItem, 0, len(lines))}
	for _, line := range lines {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pos_table_check_items(check_id,outlet_id,product_id,revision,quantity)
 VALUES($1,$2,$3,$4,$5)`, id, outlet, line.ProductID, line.Revision, line.Quantity); err != nil {
			return 0, nil, err
		}
		result.Items = append(result.Items, posSaleItem{posLine{line.ProductID, line.Revision, line.Quantity}, line.Name, line.UnitMinor})
	}
	return http.StatusCreated, result, nil
}

func readPOSTableCurrentCatalog(ctx context.Context, tx *sql.Tx, outlet int64, ids []int64) ([]tablecheck.CatalogItem, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id,v.name,v.price_minor,v.revision,v.active
 FROM pos_products p JOIN pos_product_versions v ON v.product_id=p.id
 AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
 WHERE p.outlet_id=$1 AND p.id=ANY($2::bigint[])`, outlet, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	catalog := make([]tablecheck.CatalogItem, 0, len(ids))
	for rows.Next() {
		item := tablecheck.CatalogItem{OutletID: outlet}
		if err := rows.Scan(&item.ProductID, &item.Name, &item.UnitMinor, &item.Revision, &item.Active); err != nil {
			return nil, err
		}
		catalog = append(catalog, item)
	}
	return catalog, rows.Err()
}

func mcpPOSTableCheck(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if !posSellingRole(s.role) {
		return nil, forbiddenRole
	}
	if !s.outlet.Valid {
		return nil, missingOutlet
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	number, err := pathID(r, "table")
	if err != nil || number < 1 || number > tablecheck.MaxTable || strconv.FormatInt(number, 10) != r.PathValue("table") || r.URL.RawQuery != "" {
		return nil, badInput
	}
	var total int64
	err = tx.QueryRowContext(r.Context(), `SELECT c.total_minor FROM pos_table_check_slots s JOIN pos_table_checks c ON c.id=s.check_id WHERE s.outlet_id=$1 AND s.table_number=$2`,
		outlet, number).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, posTableCheckMissing
	}
	if err != nil {
		return nil, err
	}
	return struct {
		Table int64  `json:"table_number"`
		Total int64  `json:"total_minor"`
		State string `json:"state"`
		URL   string `json:"handoff_url"`
	}{number, total, "untendered", fmt.Sprintf("/pos#entity=%d&outlet=%d&table=%d", s.ID, outlet, number)}, nil
}
