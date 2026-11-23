package main

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func mcpPOSCatalogProduct(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
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
	out := mcpclient.POSProductDetails{Currency: "ARS"}
	err = tx.QueryRowContext(r.Context(), currentOutletPOSProducts+` AND p.id=$2`, outlet, id).
		Scan(&out.ID, &out.Name, &out.PriceMinor, &out.Revision, &out.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, posMissingProduct
	}
	return out, err
}
