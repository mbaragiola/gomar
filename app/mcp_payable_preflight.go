package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/documentref"
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
	document, err := pathID(r, "document")
	if err != nil || document <= 0 {
		return nil, missingDocumentDetail
	}
	view, reference, err := readPayableEligibility(r.Context(), tx, s.entityMembership, outlet, document)
	if err != nil {
		return nil, err
	}
	// A wider read key still gets the same eligibility result but never a
	// handoff. The browser locator is reserved for a single pinned outlet.
	if s.outlet.Valid && view.Eligible && reference.Valid() {
		view.HandoffURL = mcpclient.PayableEligibilityHandoffURL(s.ID, outlet, document)
	}
	return view, nil
}

func readPayableEligibility(ctx context.Context, tx *sql.Tx, m entityMembership, outlet, document int64) (mcpclient.PayablePreflight, documentref.Reference, error) {
	var status string
	var total int64
	var posted, supplierMatches bool
	var ref documentref.Reference
	// Match the native predicate on the exact current outlet. The query
	// uses identifiers only as predicates and never returns a CUIT.
	err := tx.QueryRowContext(ctx, `SELECT d.status,d.document_type,d.total_minor,
		d.issuer_cuit COLLATE "C" = s.cuit COLLATE "C",
		EXISTS(SELECT 1 FROM ap_payables p WHERE p.source_document_id=d.id),
		d.letter,d.point_of_sale,d.number`+documentScope+` AND d.id=$4`,
		m.ID, m.id, outlet, document).
		Scan(&status, &ref.DocumentType, &total, &supplierMatches, &posted, &ref.Letter, &ref.PointOfSale, &ref.Number)
	if errors.Is(err, sql.ErrNoRows) {
		err = missingDocumentDetail
	}
	if err != nil {
		return mcpclient.PayablePreflight{}, documentref.Reference{}, err
	}
	blocker, err := payableDocumentEligibility(posted, status, ref.DocumentType, total, supplierMatches)
	return mcpclient.PayablePreflight{Eligible: err == nil, Blocker: blocker}, ref, nil
}
