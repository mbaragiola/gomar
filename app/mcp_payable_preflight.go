package main

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func mcpPayablePreflight(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpFinanceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, badInput
	}
	missing := problem{404, "No se encontró el comprobante."}
	document, err := pathID(r, "document")
	if err != nil || document <= 0 {
		return nil, missing
	}
	var status, kind string
	var total int64
	var posted, supplierMatches bool
	// Use the same native joins, but pin the exact outlet. Read only the facts
	// needed for the predicate, not supplier identifiers or tax numbers.
	err = tx.QueryRowContext(r.Context(), `SELECT d.status,d.document_type,d.total_minor,
		d.issuer_cuit COLLATE "C" = s.cuit COLLATE "C",
		EXISTS(SELECT 1 FROM ap_payables p WHERE p.source_document_id=d.id)`+
		documentScope+` AND d.id=$4`, s.ID, s.id, outlet, document).
		Scan(&status, &kind, &total, &supplierMatches, &posted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missing
	}
	if err != nil {
		return nil, err
	}
	blocker, err := payableDocumentEligibility(posted, status, kind, total, supplierMatches)
	return mcpclient.PayablePreflight{Eligible: err == nil, Blocker: blocker}, nil
}
