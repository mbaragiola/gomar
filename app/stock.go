package main

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"time"
)

const stockPath = posPath + "/stock"
const stockQuantityMax = 1_000_000

var (
	stockNotTracked = problem{409, "El stock no está activo para este producto y local."}
	stockConflict   = problem{409, "No se puede volver a usar el identificador de este movimiento."}
)

type stockProduct struct {
	ID      int64  `json:"producto_id"`
	Name    string `json:"nombre"`
	Enabled bool   `json:"activo"`
	Minimum int64  `json:"minimo"`
	Balance int64  `json:"saldo"`
	Warning string `json:"aviso,omitempty"`
}
type stockMovement struct {
	ID         int64  `json:"id"`
	Kind       string `json:"tipo"`
	Quantity   int64  `json:"cantidad"`
	Delta      int64  `json:"variacion"`
	RecordedAt string `json:"creado_en"`
}
type stockInput struct {
	RequestID string `json:"id_solicitud"`
	Kind      string `json:"tipo"`
	Quantity  *int64 `json:"cantidad"`
}

func (a *app) registerStock(add func(string, string, http.HandlerFunc), enabled bool) {
	if !enabled {
		return
	}
	route := func(admin, body bool, next scopedHandler) http.HandlerFunc {
		capability := "read_business_data"
		if admin {
			capability = "manage_entity_access"
		}
		if body {
			next = a.lockOutletForOperation(next)
		}
		handler := a.scopedRequest(capability, func(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
			switch m.role {
			case "administration", "purchasing", "accounting":
			default:
				return 0, nil, forbiddenRole
			}
			return next(r, tx, m, raw)
		}, body)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			handler(w, r)
		}
	}
	add(stockPath, "GET", route(false, false, listStock))
	add(stockPath+"/settings", "PUT", route(true, true, saveStockOutlet))
	add(stockPath+"/products/{product}", "PUT", route(true, true, saveStockProduct))
	add(stockPath+"/products/{product}/movements", "GET", route(false, false, listStockMovements))
	add(stockPath+"/products/{product}/movements", "POST", route(false, true, postStockMovement))
}

func stockOutletActive(ctx context.Context, tx *sql.Tx, outlet int64) (bool, error) {
	var enabled bool
	err := tx.QueryRowContext(ctx, "SELECT enabled FROM stock_outlets WHERE outlet_id=$1", outlet).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return enabled, err
}

func listStock(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	ctx := r.Context()
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	active, err := stockOutletActive(ctx, tx, outlet)
	if err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.product_id,v.name,t.enabled,t.minimum,
        coalesce((SELECT CAST(sum(delta) AS BIGINT) FROM stock_movements x WHERE x.outlet_id=t.outlet_id AND x.product_id=t.product_id),0)
        FROM stock_products t JOIN pos_products p ON p.id=t.product_id AND p.outlet_id=t.outlet_id
        JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
        WHERE t.outlet_id=$1 ORDER BY t.product_id LIMIT $2`, outlet, posCatalogMax)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	products := []stockProduct{}
	for rows.Next() {
		var p stockProduct
		if err := rows.Scan(&p.ID, &p.Name, &p.Enabled, &p.Minimum, &p.Balance); err != nil {
			return 0, nil, err
		}
		if active && p.Enabled {
			if p.Balance < 0 {
				p.Warning = "negativo"
			} else if p.Balance <= p.Minimum {
				p.Warning = "bajo"
			}
		}
		products = append(products, p)
	}
	return 200, struct {
		Enabled  bool           `json:"activo"`
		Products []stockProduct `json:"productos"`
	}{active, products}, rows.Err()
}

func saveStockOutlet(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	var input struct {
		Enabled *bool `json:"activo"`
	}
	if err := decodePayables(raw, &input, "activo"); err != nil {
		return 0, nil, err
	}
	if input.Enabled == nil {
		return 0, nil, badInput
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO stock_outlets(outlet_id,enabled) VALUES($1,$2)
        ON CONFLICT(outlet_id) DO UPDATE SET enabled=excluded.enabled`, outlet, *input.Enabled)
	return 200, input, err
}

func saveStockProduct(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	ctx := r.Context()
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	id, err := pathID(r, "product")
	if err != nil {
		return 0, nil, err
	}
	var input struct {
		Enabled *bool  `json:"activo"`
		Minimum *int64 `json:"minimo"`
	}
	if err := decodePayables(raw, &input, "activo", "minimo"); err != nil {
		return 0, nil, err
	}
	if input.Enabled == nil || input.Minimum == nil || *input.Minimum < 0 || *input.Minimum > stockQuantityMax {
		return 0, nil, badInput
	}
	var found bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pos_products WHERE outlet_id=$1 AND id=$2)`, outlet, id).Scan(&found); err != nil {
		return 0, nil, err
	}
	if !found {
		return 0, nil, posMissingProduct
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO stock_outlets(outlet_id,enabled) VALUES($1,FALSE)
        ON CONFLICT(outlet_id) DO NOTHING`, outlet); err != nil {
		return 0, nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO stock_products(outlet_id,product_id,enabled,minimum) VALUES($1,$2,$3,$4)
        ON CONFLICT(outlet_id,product_id) DO UPDATE SET enabled=excluded.enabled,minimum=excluded.minimum`, outlet, id, *input.Enabled, *input.Minimum)
	return 200, input, err
}

func listStockMovements(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	id, err := pathID(r, "product")
	if err != nil {
		return 0, nil, err
	}
	var found bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM stock_products WHERE outlet_id=$1 AND product_id=$2)`, outlet, id).Scan(&found); err != nil {
		return 0, nil, err
	}
	if !found {
		return 0, nil, posMissingProduct
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,kind,quantity,delta,recorded_at FROM stock_movements
        WHERE outlet_id=$1 AND product_id=$2 ORDER BY id DESC LIMIT 101`, outlet, id)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	moves := []stockMovement{}
	for rows.Next() {
		var x stockMovement
		if err := rows.Scan(&x.ID, &x.Kind, &x.Quantity, &x.Delta, &x.RecordedAt); err != nil {
			return 0, nil, err
		}
		moves = append(moves, x)
	}
	truncated := len(moves) > 100
	if truncated {
		moves = moves[:100]
	}
	return 200, struct {
		Movements []stockMovement `json:"movimientos"`
		Truncated bool            `json:"truncado"`
	}{moves, truncated}, rows.Err()
}

func postStockMovement(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	id, err := pathID(r, "product")
	if err != nil {
		return 0, nil, err
	}
	var input stockInput
	if err := decodePayables(raw, &input, "id_solicitud", "tipo", "cantidad"); err != nil {
		return 0, nil, err
	}
	return recordStockMovement(r.Context(), tx, outlet, id, m.userID, input, time.Now())
}

func recordStockMovement(ctx context.Context, tx *sql.Tx, outlet, id, actor int64, input stockInput, now time.Time) (int, any, error) {
	var x stockMovement
	if !posRequestID.MatchString(input.RequestID) || input.Quantity == nil || *input.Quantity < 0 || *input.Quantity > stockQuantityMax {
		return 0, nil, badInput
	}
	qty := *input.Quantity
	switch input.Kind {
	case "recepcion", "merma":
		if qty == 0 {
			return 0, nil, badInput
		}
	case "conteo":
	default:
		return 0, nil, badInput
	}
	var existingProduct, existingActor int64
	err := tx.QueryRowContext(ctx, `SELECT id,product_id,actor_id,kind,quantity,delta,recorded_at
        FROM stock_movements WHERE outlet_id=$1 AND request_id=$2`, outlet, input.RequestID).
		Scan(&x.ID, &existingProduct, &existingActor, &x.Kind, &x.Quantity, &x.Delta, &x.RecordedAt)
	if err == nil {
		if existingProduct != id || existingActor != actor || x.Kind != input.Kind || x.Quantity != qty {
			return 0, nil, stockConflict
		}
		return 200, x, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT t.enabled AND o.enabled FROM stock_products t
        JOIN stock_outlets o ON o.outlet_id=t.outlet_id WHERE t.outlet_id=$1 AND t.product_id=$2`, outlet, id).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !enabled) {
		return 0, nil, stockNotTracked
	}
	if err != nil {
		return 0, nil, err
	}
	x.Kind, x.Quantity, x.RecordedAt = input.Kind, qty, now.UTC().Format(time.RFC3339Nano)
	switch input.Kind {
	case "recepcion":
		x.Delta = qty
	case "merma":
		x.Delta = -qty
	case "conteo":
		var balance int64
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(CAST(sum(delta) AS BIGINT),0) FROM stock_movements WHERE outlet_id=$1 AND product_id=$2`, outlet, id).Scan(&balance); err != nil {
			return 0, nil, err
		}
		// At this practical scale a count cannot overflow int64.
		if balance < qty-math.MaxInt64 {
			return 0, nil, errors.New("stock balance outside supported range")
		}
		x.Delta = qty - balance
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, outlet, id, actor, x.Kind, x.Quantity, x.Delta, x.RecordedAt, input.RequestID).Scan(&x.ID)
	return 201, x, err
}

// Called only once per new POS sale, after writing immutable items and before
// committing. Negative estimates are valid: stock never rejects a sale for lack
// of units, including an older offline cart queued before reconnection.
func recordPOSStockSale(ctx context.Context, tx *sql.Tx, outletID, saleID int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,sale_id)
        SELECT i.outlet_id,i.product_id,s.actor_id,'venta',i.quantity,-i.quantity,s.recorded_at,s.id
        FROM pos_sale_items i
        JOIN pos_sales s ON s.id=i.sale_id AND s.outlet_id=i.outlet_id
        JOIN stock_products t ON t.product_id=i.product_id AND t.outlet_id=i.outlet_id AND t.enabled=TRUE
        JOIN stock_outlets o ON o.outlet_id=i.outlet_id AND o.enabled=TRUE
        WHERE i.sale_id=$1 AND i.outlet_id=$2
        ON CONFLICT(sale_id,product_id) DO NOTHING`, saleID, outletID)
	return err
}
