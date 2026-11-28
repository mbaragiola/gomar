package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mbaragiola/gomar/app/internal/documentref"
	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

const removedDraftScope = ` FROM documents d
	WHERE d.entity_id=$1 AND d.outlet_id=$2 AND d.status='draft' AND d.deleted_at IS NOT NULL
	AND NOT EXISTS (SELECT 1 FROM ap_payables p WHERE p.source_document_id=d.id)`

type removedDraft struct {
	ID         int64  `json:"id"`
	Revision   int64  `json:"revision"`
	RemovedAt  string `json:"removed_at"`
	IssuerCUIT string `json:"issuer_cuit,omitempty"`
	documentref.Reference
	HandoffURL string `json:"handoff_url,omitempty"`
}

type removedDraftPage struct {
	Items  []removedDraft `json:"items"`
	Before *int64         `json:"next_before_id,omitempty"`
}

func listRemovedDrafts(r *http.Request, tx *sql.Tx, member entityMembership, _ []byte) (int, any, error) {
	outletID, err := pathID(r, "outlet")
	if err != nil {
		return 0, nil, err
	}
	if err := requireSelectedDocumentOutlet(tx, member, outletID); err != nil {
		return 0, nil, err
	}
	result, err := readRemovedDrafts(r, tx, member, outletID, false)
	return http.StatusOK, result, err
}

func mcpRemovedDrafts(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if !slices.Contains(capabilities[s.role], draftCapability) {
		return nil, forbiddenRole
	}
	outletID, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	return readRemovedDrafts(r, tx, s.entityMembership, outletID, true)
}

func readRemovedDrafts(r *http.Request, tx *sql.Tx, member entityMembership, outletID int64, forMCP bool) (any, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, badInput
	}
	var beforeID, targetID sql.NullInt64
	for key, values := range query {
		if len(query) != 1 || len(values) != 1 {
			return nil, badInput
		}
		id, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || id < 1 || values[0] != strconv.FormatInt(id, 10) {
			return nil, badInput
		}
		value := sql.NullInt64{Int64: id, Valid: true}
		switch {
		case key == "before_id":
			beforeID = value
		case key == "draft_id" && !forMCP:
			targetID = value
		default:
			return nil, badInput
		}
	}
	const pageSize = mcpclient.RemovedDraftPageSize
	rows, err := tx.QueryContext(r.Context(), `SELECT d.id,d.revision,d.deleted_at,d.document_type,d.letter,d.point_of_sale,d.number,
		CASE WHEN $5 THEN '' ELSE d.issuer_cuit END`+removedDraftScope+
		` AND ($3::bigint IS NULL OR d.id < $3) AND ($4::bigint IS NULL OR d.id = $4)
		AND EXISTS (SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
			WHERE g.membership_id=$6 AND o.id=d.outlet_id AND o.entity_id=d.entity_id AND o.active)
		ORDER BY d.id DESC LIMIT $7`, member.ID, outletID, beforeID, targetID, forMCP, member.id, pageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := removedDraftPage{Items: make([]removedDraft, 0)}
	for rows.Next() {
		var row removedDraft
		if err := rows.Scan(&row.ID, &row.Revision, &row.RemovedAt, &row.DocumentType, &row.Letter, &row.PointOfSale, &row.Number, &row.IssuerCUIT); err != nil {
			return nil, err
		}
		if len(result.Items) == pageSize {
			result.Before = &result.Items[pageSize-1].ID
			break
		}
		if forMCP {
			row.HandoffURL = mcpclient.RemovedDraftHandoffURL(member.ID, outletID, row.ID)
		}
		result.Items = append(result.Items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if targetID.Valid && len(result.Items) == 0 {
		return nil, missingRemovedDraft
	}
	return result, nil
}

var missingRemovedDraft = problem{404, "No se encontró el borrador dado de baja."}

func (a *app) restoreRemovedDraft(w http.ResponseWriter, r *http.Request, u user) error {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	outletID, err := pathID(r, "outlet")
	if err != nil {
		return err
	}
	documentID, err := pathID(r, "document")
	if err != nil {
		return err
	}
	var restore draftMutationFields
	if err := parseJSON(w, r, &restore); err != nil {
		return err
	}
	if restore.Reason == nil || restore.Revision == nil {
		return badInput
	}
	reason := trimDraftText(*restore.Reason)
	if reason == "" {
		return problem{422, "El motivo de la restauración es obligatorio."}
	}
	if utf8.RuneCountInString(reason) > 2000 || strings.ContainsRune(reason, 0) {
		return problem{422, "El motivo de la restauración no es válido."}
	}
	tx, err := a.beginEmployeeCheckedWrite(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entityID, draftCapability)
	if err != nil {
		return err
	}
	if err := requireSelectedDocumentOutlet(tx, member, outletID); err != nil {
		return err
	}
	if err := a.lockDocumentWrite(r, tx, u, entityID); err != nil {
		return err
	}
	member, err = scopedMembership(tx, u.id, entityID, draftCapability)
	if err != nil {
		return err
	}
	if err := requireSelectedDocumentOutlet(tx, member, outletID); err != nil {
		return err
	}
	var current documentListItem
	err = tx.QueryRowContext(r.Context(), `SELECT d.revision`+removedDraftScope+` AND d.id=$3`, entityID, outletID, documentID).Scan(&current.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return missingRemovedDraft
	}
	if err != nil {
		return err
	}
	if err := validateDraftRevision(current, *restore.Revision); err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(r.Context(), `UPDATE documents SET deleted_at=NULL,deleted_by_id=NULL,updated_by_id=$1,updated_at=$2,
		revision=revision+1 WHERE id=$3 AND revision=$4`, member.userID, at, documentID, current.Revision)
	if isDocumentIdentityConflict(err) {
		return duplicateDocument
	}
	if err != nil {
		return err
	}
	if err := requireDraftChange(result); err != nil {
		return err
	}
	if err := insertDocumentEvent(tx, documentID, member.userID, "restore", reason, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, map[string]int64{"id": documentID, "revision": current.Revision + 1})
	return nil
}
