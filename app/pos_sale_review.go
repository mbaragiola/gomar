package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/postime"
)

type posSaleReviewReceipt struct {
	ID             int64           `json:"id"`
	RequestID      string          `json:"id_solicitud"`
	Actor          int64           `json:"actor_id"`
	Total          int64           `json:"total_centavos"`
	PaymentMethod  string          `json:"metodo_pago"`
	Items          json.RawMessage `json:"items"`
	Recorded       string          `json:"creado_en"`
	TillReported   string          `json:"hora_informada_caja_utc,omitempty"`
	TillTimeStatus string          `json:"estado_hora_caja"`
}

func (a *app) readPOSSaleReceipt(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	if !posSellingRole(m.role) {
		return 0, nil, forbiddenRole
	}
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	row, err := a.scopedPOSSaleRow(r, tx, m, outlet, `s.id,s.actor_id,s.total_minor,s.payment_method,s.recorded_at,
		COALESCE(s.till_reported_at_utc,''),
		COALESCE((SELECT jsonb_agg(jsonb_build_object('producto_id',i.product_id,'revision',i.revision,'cantidad',i.quantity) ORDER BY i.product_id)
		 FROM pos_sale_items i WHERE i.sale_id=s.id AND i.outlet_id=s.outlet_id),'[]'::jsonb)`)
	if err != nil {
		return 0, nil, err
	}
	receipt := posSaleReviewReceipt{RequestID: r.PathValue("request")}
	err = row.Scan(&receipt.ID, &receipt.Actor, &receipt.Total, &receipt.PaymentMethod, &receipt.Recorded, &receipt.TillReported, &receipt.Items)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, posSaleTimeMissing
	}
	if err != nil {
		return 0, nil, err
	}
	registered, ok := postime.ParseUTC(receipt.Recorded)
	if !ok {
		return 0, nil, errors.New("invalid registered sale time")
	}
	receipt.TillTimeStatus, ok = postime.Status(receipt.TillReported, registered)
	if !ok {
		return 0, nil, errors.New("invalid till-reported sale time")
	}
	return http.StatusOK, receipt, nil
}
