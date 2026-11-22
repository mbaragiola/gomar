package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
)

type reconciliationDetailPage struct {
	Items []json.RawMessage `json:"items"`
	Total int64             `json:"total"`
}

func reconciliationPageNumber(r *http.Request, key string, defaultValue int64) (int64, error) {
	values, ok := r.URL.Query()[key]
	if !ok {
		return defaultValue, nil
	}
	value, err := strconv.ParseInt(values[len(values)-1], 10, 64)
	if err != nil {
		return 0, badInput
	}
	return max(value, 1), nil
}

func reconciliationOffset(page, size int64) int64 {
	if page-1 > math.MaxInt64/size {
		return math.MaxInt64
	}
	return (page - 1) * size
}

func readReconciliationPage(tx *sql.Tx, from, order string, args []any, size, offset int64) (reconciliationDetailPage, error) {
	result := reconciliationDetailPage{Items: []json.RawMessage{}}
	if err := tx.QueryRow("SELECT COUNT(*) "+from, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	if offset >= result.Total {
		return result, nil
	}
	rows, err := tx.Query("SELECT payload "+from+" ORDER BY "+order+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), append(args, size, offset)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return result, err
		}
		result.Items = append(result.Items, json.RawMessage(raw))
	}
	return result, rows.Err()
}

func (a *app) readReconciliationSection(w http.ResponseWriter, r *http.Request, u user, section string) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	id, err := pathID(r, "run")
	if err != nil {
		return err
	}
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := scopedMembership(tx, u.id, entity, "reconcile_financial_data")
	if err != nil {
		return err
	}
	runs, err := scanReconciliationRuns(tx, m, id)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return problem{404, "No se encontró la conciliación."}
	}
	page, err := reconciliationPageNumber(r, "page", 1)
	if err != nil {
		return err
	}
	defaultSize := int64(50)
	if section == "candidates" {
		defaultSize = 25
	}
	size, err := reconciliationPageNumber(r, "page_size", defaultSize)
	if err != nil {
		return err
	}
	size = min(size, 100)
	offset := reconciliationOffset(page, size)
	args := []any{id}
	var body any
	switch section {
	case "rows":
		from := `FROM reconciliation_detail_rows rr JOIN reconciliation_read_sources s
			ON s.run_id=rr.run_id AND s.id=rr.source_id WHERE rr.run_id=$1`
		if side := r.URL.Query().Get("side"); side != "" {
			if side != "payments" && side != "cash_flow" {
				return problem{422, "El tipo de archivo no es válido."}
			}
			from += " AND s.side=$2"
			args = append(args, side)
		}
		body, err = readReconciliationPage(tx, from, "rr.source_id,rr.record_number", args, size, offset)
	case "events":
		eventView := `FROM (SELECT e.run_id,e.ordinal,
			CASE WHEN u.id IS NULL THEN e.payload ELSE CAST(jsonb_set(CAST(e.payload AS jsonb),'{actor}',to_jsonb(u.username)) AS text) END AS payload
			FROM reconciliation_detail_events e LEFT JOIN users u ON u.id=e.actor_id) WHERE run_id=$1`
		body, err = readReconciliationPage(tx, eventView, "ordinal", args, size, offset)
	case "candidates":
		body, err = readReconciliationCandidatePage(tx, r, id, size, offset)
	default:
		return fmt.Errorf("unknown reconciliation section %q", section)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, body)
	return nil
}

type reconciliationCandidateRead struct {
	ID            int64             `json:"group_id"`
	Kind          string            `json:"kind"`
	Payments      []json.RawMessage `json:"payments"`
	CashFlow      []json.RawMessage `json:"cash_flow"`
	PaymentCount  int64             `json:"payment_count"`
	CashFlowCount int64             `json:"cash_flow_count"`
	Decisions     []json.RawMessage `json:"decisions"`
	Discrepancies *[]string         `json:"discrepancies,omitempty"`
	Comparisons   map[string]string `json:"comparisons,omitempty"`
}

func readReconciliationCandidatePage(tx *sql.Tx, r *http.Request, id, size, offset int64) (any, error) {
	paymentPage, err := reconciliationPageNumber(r, "payment_member_page", 1)
	if err != nil {
		return nil, err
	}
	cashPage, err := reconciliationPageNumber(r, "cash_flow_member_page", 1)
	if err != nil {
		return nil, err
	}
	memberSize, err := reconciliationPageNumber(r, "member_page_size", 50)
	if err != nil {
		return nil, err
	}
	memberSize = min(memberSize, 100)
	result := struct {
		Items []reconciliationCandidateRead `json:"items"`
		Total int64                         `json:"total"`
	}{Items: []reconciliationCandidateRead{}}
	if err := tx.QueryRow("SELECT COUNT(*) FROM reconciliation_detail_groups WHERE run_id=$1", id).Scan(&result.Total); err != nil {
		return nil, err
	}
	if result.Total <= offset {
		return result, nil
	}
	rows, err := tx.Query("SELECT ordinal,payload FROM reconciliation_detail_groups WHERE run_id=$1 ORDER BY ordinal LIMIT $2 OFFSET $3", id, size, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate reconciliationCandidateRead
		var ordinal int64
		var payload []byte
		if err := rows.Scan(&ordinal, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &candidate); err != nil {
			return nil, err
		}
		candidate.ID = ordinal
		if candidate.Decisions == nil {
			candidate.Decisions = []json.RawMessage{}
		}
		result.Items = append(result.Items, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	const from = `FROM reconciliation_detail_members g
		JOIN reconciliation_detail_rows p ON p.run_id=g.run_id AND p.id=g.row_id
		JOIN reconciliation_read_sources s ON s.run_id=p.run_id AND s.id=p.source_id
		WHERE g.run_id=$1 AND g.group_ordinal=$2 AND s.side=$3`
	for i := range result.Items {
		candidate := &result.Items[i]
		payments, err := readReconciliationPage(tx, from, "g.ordinal,p.source_id,p.record_number", []any{id, candidate.ID, "payments"}, memberSize, reconciliationOffset(paymentPage, memberSize))
		if err != nil {
			return nil, err
		}
		cash, err := readReconciliationPage(tx, from, "g.ordinal,p.source_id,p.record_number", []any{id, candidate.ID, "cash_flow"}, memberSize, reconciliationOffset(cashPage, memberSize))
		if err != nil {
			return nil, err
		}
		candidate.Payments, candidate.PaymentCount = payments.Items, payments.Total
		candidate.CashFlow, candidate.CashFlowCount = cash.Items, cash.Total
		decisions, err := readReconciliationDecisions(tx, id, candidate.ID)
		if err != nil {
			return nil, err
		}
		candidate.Decisions = append(candidate.Decisions, decisions...)
	}
	return result, nil
}
