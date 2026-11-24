package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/mbaragiola/gomar/app/internal/postender"
	"github.com/mbaragiola/gomar/app/internal/tablecheck"
)

const maxHistoricalTenderRecords = 500 // A correction remains possible for every recorded leg.

var (
	posTableTenderBlocksRetirement = problem{409, "La cuenta tiene registros de pago. No se puede retirar."}
	posTableTenderConflict         = problem{409, "El registro cambió o ya existe. Volvé a consultar la cuenta."}
	posTableTenderOverpay          = problem{422, "El importe supera el saldo de esta cuenta."}
	posTableTenderLimit            = problem{422, "La cuenta alcanzó el límite de registros de pago."}
)

type posTenderTarget struct {
	scope postender.Scope
	total int64
}

type posTenderEvent struct {
	ID     string               `json:"id_registro"`
	Kind   string               `json:"tipo"`
	Method string               `json:"medio,omitempty"`
	Amount int64                `json:"importe_centavos,omitempty"`
	Target string               `json:"registro_original,omitempty"`
	Reason postender.VoidReason `json:"motivo,omitempty"`
}

type posTenderView struct {
	Check       string           `json:"id_cuenta"`
	Recorded    int64            `json:"registrado_centavos"`
	Outstanding int64            `json:"saldo_centavos"`
	Events      []posTenderEvent `json:"registros"`
}

type posTenderCommand struct {
	Check  string                `json:"id_cuenta"`
	ID     string                `json:"id_solicitud"`
	Kind   string                `json:"tipo"`
	Amount *int64                `json:"importe_centavos"`
	Method *string               `json:"medio"`
	Target *string               `json:"registro_original"`
	Reason *postender.VoidReason `json:"motivo"`
}

func posCanonicalID(raw string) int64 {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != raw {
		return 0
	}
	return id
}

// If write is true, the caller already holds the outlet lock used to save
// and retire checks. Take the exact active slot next, never a foreign row.
func loadPOSTenderTarget(r *http.Request, tx *sql.Tx, m entityMembership, write bool) (posTenderTarget, error) {
	var t posTenderTarget
	if m.employee || !posSellingRole(m.role) {
		return t, forbiddenRole
	}
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return t, err
	}
	number := posCanonicalID(r.PathValue("table"))
	if number < 1 || number > tablecheck.MaxTable || r.URL.RawQuery != "" {
		return t, badInput
	}
	t.scope.EntityID, t.scope.OutletID = m.ID, outlet
	query := `SELECT s.check_id,c.total_minor FROM pos_table_check_slots s
		JOIN pos_table_checks c ON c.id=s.check_id AND c.outlet_id=s.outlet_id
		WHERE s.outlet_id=$1 AND s.table_number=$2`
	if write {
		query += " FOR UPDATE OF s"
	}
	err = tx.QueryRowContext(r.Context(), query, outlet, number).Scan(&t.scope.CheckID, &t.total)
	if errors.Is(err, sql.ErrNoRows) {
		return t, posTableCheckMissing
	}
	return t, err
}

func loadPOSTenderHistory(ctx context.Context, tx *sql.Tx, t posTenderTarget) (*postender.Journal, []posTenderEvent, map[int64]string, error) {
	j, err := postender.New(t.scope, t.total)
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,actor_id,request_id,kind,COALESCE(amount_minor,0),
		COALESCE(method,''),COALESCE(target_id,0),COALESCE(reason,'') FROM pos_table_tender_events
		WHERE outlet_id=$1 AND check_id=$2 ORDER BY id LIMIT $3`,
		t.scope.OutletID, t.scope.CheckID, 2*maxHistoricalTenderRecords+1)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	events := []posTenderEvent{}
	requestByEvent := map[int64]string{}
	for rows.Next() {
		if len(events) >= 2*maxHistoricalTenderRecords {
			return nil, nil, nil, fmt.Errorf("manual tender history exceeds supported size")
		}
		var e posTenderEvent
		var id, target int64
		req := postender.Request{Scope: t.scope}
		if err := rows.Scan(&id, &req.ActorID, &req.RequestID, &e.Kind, &e.Amount, &e.Method, &target, &e.Reason); err != nil {
			return nil, nil, nil, err
		}
		e.ID = strconv.FormatInt(id, 10)
		var replay bool
		switch e.Kind {
		case "record":
			replay, err = j.Record(postender.Leg{Request: req, Method: e.Method, AmountMinor: e.Amount})
			requestByEvent[id] = req.RequestID
		case "void":
			e.Target = strconv.FormatInt(target, 10)
			replay, err = j.Void(postender.Void{Request: req, TargetRequestID: requestByEvent[target], Reason: e.Reason})
		default:
			return nil, nil, nil, fmt.Errorf("unknown manual tender event")
		}
		if err != nil || replay {
			return nil, nil, nil, fmt.Errorf("invalid stored manual tender history")
		}
		events = append(events, e)
	}
	return j, events, requestByEvent, rows.Err()
}

func (a *app) readPOSTender(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	t, err := loadPOSTenderTarget(r, tx, m, false)
	if err != nil {
		return 0, nil, err
	}
	j, history, _, err := loadPOSTenderHistory(r.Context(), tx, t)
	if err != nil {
		return 0, nil, err
	}
	b := j.Balance()
	return http.StatusOK, posTenderView{strconv.FormatInt(t.scope.CheckID, 10), b.RecordedMinor, b.OutstandingMinor, history}, nil
}

func (a *app) lockPOSTenderForWrite(next scopedHandler) scopedHandler {
	guarded := lockPOSWriteScope("read_business_data", true, next)
	return func(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
		if m.employee {
			return 0, nil, forbiddenRole
		}
		return guarded(r, tx, m, raw)
	}
}

func (a *app) writePOSTender(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	t, err := loadPOSTenderTarget(r, tx, m, true)
	if err != nil {
		return 0, nil, err
	}
	var c posTenderCommand
	if len(raw) > 640 || json.Unmarshal(raw, &c) != nil {
		return 0, nil, badInput
	}
	fields := []string{"id_cuenta", "id_solicitud", "tipo"}
	switch c.Kind {
	case "record":
		fields = append(fields, "importe_centavos", "medio")
	case "void":
		fields = append(fields, "registro_original", "motivo")
	default:
		return 0, nil, badInput
	}
	if err := decodeWikiProposalJSON(raw, &c, fields...); err != nil {
		return 0, nil, err
	}
	if !posRequestID.MatchString(c.ID) || posCanonicalID(c.Check) == 0 {
		return 0, nil, badInput
	}
	if posCanonicalID(c.Check) != t.scope.CheckID {
		return 0, nil, posTableTenderConflict
	}
	var amount, target int64
	var method string
	var reason postender.VoidReason
	switch c.Kind {
	case "record":
		if c.Amount == nil || *c.Amount <= 0 || c.Method == nil || !postender.IsManualMethod(*c.Method) || c.Target != nil || c.Reason != nil {
			return 0, nil, badInput
		}
		amount, method = *c.Amount, *c.Method
	case "void":
		if c.Amount != nil || c.Method != nil || c.Target == nil || posCanonicalID(*c.Target) == 0 || c.Reason == nil || !c.Reason.Valid() {
			return 0, nil, badInput
		}
		target, reason = posCanonicalID(*c.Target), *c.Reason
	default:
		return 0, nil, badInput
	}
	ctx := r.Context()
	var savedID, savedCheck, savedActor, savedAmount, savedTarget int64
	var savedKind, savedMethod string
	var savedReason postender.VoidReason
	err = tx.QueryRowContext(ctx, `SELECT id,check_id,actor_id,kind,COALESCE(amount_minor,0),COALESCE(method,''),
		COALESCE(target_id,0),COALESCE(reason,'') FROM pos_table_tender_events WHERE outlet_id=$1 AND request_id=$2`,
		t.scope.OutletID, c.ID).Scan(&savedID, &savedCheck, &savedActor, &savedKind, &savedAmount, &savedMethod, &savedTarget, &savedReason)
	if err == nil {
		if savedCheck != t.scope.CheckID || savedActor != m.userID || savedKind != c.Kind || savedAmount != amount ||
			savedMethod != method || savedTarget != target || savedReason != reason {
			return 0, nil, posTableTenderConflict
		}
		return http.StatusOK, tenderEventReceipt(savedID, c.Check), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	var used bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pos_table_finalizations f
 JOIN pos_sales s ON s.id=f.sale_id AND s.outlet_id=f.outlet_id WHERE f.outlet_id=$1 AND s.request_id=$2)`, t.scope.OutletID, c.ID).Scan(&used); err != nil {
		return 0, nil, err
	}
	if used {
		return 0, nil, posTableTenderConflict
	}
	j, _, requestByEvent, err := loadPOSTenderHistory(ctx, tx, t)
	if err != nil {
		return 0, nil, err
	}
	request := postender.Request{Scope: t.scope, ActorID: m.userID, RequestID: c.ID}
	switch c.Kind {
	case "record":
		if len(requestByEvent) >= maxHistoricalTenderRecords {
			return 0, nil, posTableTenderLimit
		}
		_, err = j.Record(postender.Leg{Request: request, Method: method, AmountMinor: amount})
	case "void":
		if requestByEvent[target] == "" {
			return 0, nil, posTableTenderConflict
		}
		_, err = j.Void(postender.Void{Request: request, TargetRequestID: requestByEvent[target], Reason: reason})
	}
	switch err {
	case nil:
	case postender.ErrOverpay:
		return 0, nil, posTableTenderOverpay
	case postender.ErrTooMany:
		return 0, nil, posTableTenderLimit
	case postender.ErrInvalid:
		return 0, nil, badInput
	default:
		return 0, nil, posTableTenderConflict
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO pos_table_tender_events(outlet_id,check_id,actor_id,request_id,kind,amount_minor,method,target_id,reason)
	VALUES($1,$2,$3,$4,$5,NULLIF($6,0),NULLIF($7,''),NULLIF($8,0),NULLIF($9,'')) RETURNING id`,
		t.scope.OutletID, t.scope.CheckID, m.userID, c.ID, c.Kind, amount, method, target, reason).Scan(&savedID)
	if isUnique(err) {
		return 0, nil, posTableTenderConflict
	}
	return http.StatusCreated, tenderEventReceipt(savedID, c.Check), err
}

func tenderEventReceipt(id int64, check string) any {
	return struct {
		ID    string `json:"id_registro"`
		Check string `json:"id_cuenta"`
	}{strconv.FormatInt(id, 10), check}
}
