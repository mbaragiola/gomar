package main

import (
	"database/sql"
	"net/http"
	"slices"

	"github.com/mbaragiola/gomar/app/internal/documentref"
)

var missingPayableEligibilityHandoff = problem{404, "No se pudo abrir un comprobante elegible en ese local."}

type payableEligibilityHandoffView struct {
	DocumentID int64                 `json:"document_id"`
	Reference  documentref.Reference `json:"printed_reference"`
}

func (a *app) payableEligibilityHandoff(w http.ResponseWriter, r *http.Request, u user) error {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !a.enableMCP || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || len(r.Header.Values("Authorization")) != 0 {
		return missingPayableEligibilityHandoff
	}
	entity, entityOK := attachmentHandoffID(r, "entity")
	outlet, outletOK := attachmentHandoffID(r, "outlet")
	document, documentOK := attachmentHandoffID(r, "document")
	if !entityOK || !outletOK || !documentOK {
		return missingPayableEligibilityHandoff
	}
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entity, "read_business_data")
	if err != nil {
		if err == missingEntity || err == forbiddenRole {
			return missingPayableEligibilityHandoff
		}
		return err
	}
	if !slices.Contains(capabilities[member.role], recordPayments) {
		return missingPayableEligibilityHandoff
	}
	view, reference, err := readPayableEligibility(r.Context(), tx, member, outlet, document)
	if err == missingDocumentDetail {
		return missingPayableEligibilityHandoff
	}
	if err != nil {
		return err
	}
	if !view.Eligible || !reference.Valid() {
		return missingPayableEligibilityHandoff
	}
	jsonResponse(w, http.StatusOK, payableEligibilityHandoffView{DocumentID: document, Reference: reference})
	return nil
}
