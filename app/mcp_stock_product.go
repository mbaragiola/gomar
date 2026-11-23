package main

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func mcpStockProduct(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if !stockReadRole(s.role) {
		return nil, forbiddenRole
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, badInput
	}
	id, err := pathID(r, "product")
	if err != nil || id <= 0 {
		return nil, posMissingProduct
	}
	result := mcpclient.StockProductDetails{}
	result.OutletEnabled, err = stockOutletActive(r.Context(), tx, outlet)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(r.Context(), `SELECT t.product_id,`+outletStockStateColumns+outletStockRows+` AND t.product_id=$2`, outlet, id).
		Scan(&result.ID, &result.ProductEnabled, &result.Minimum, &result.Balance)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, posMissingProduct
	}
	if err != nil {
		return nil, err
	}
	result.Alert = stockAlert(result.OutletEnabled, result.ProductEnabled, result.Balance, result.Minimum)
	return result, nil
}
