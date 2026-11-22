package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"
)

type stockWasteInput struct{ ProductID, Quantity int64 }
type stockPhysicalCountInput struct {
	ProductID int64
	Quantity  *int64
}
type stockAdjustmentOperation uint8

const (
	stockWaste stockAdjustmentOperation = iota + 1
	stockPhysicalCount
)

var stockAdjustmentStale = problem{409, "Cambió el stock. Prepará otra propuesta."}

type stockAdjustmentPreview struct {
	ProductID      int64  `json:"product_id"`
	ProductName    string `json:"product_name"`
	Revision       int64  `json:"revision"`
	StockMinimum   int64  `json:"stock_minimum"`
	Quantity       int64  `json:"quantity"`
	LatestMovement int64  `json:"latest_movement"`
	Before         int64  `json:"before"`
	After          int64  `json:"after"`
	Delta          int64  `json:"delta"`
}

type stockAdjustmentProposal struct {
	Operation stockAdjustmentOperation
	Scope     stockReceiptScope
	RequestID string
	Preview   stockAdjustmentPreview
}

func (p stockAdjustmentProposal) digest() [32]byte {
	raw, _ := json.Marshal(p)
	return sha256.Sum256(append([]byte("stock_adjustment.v1:"), raw...))
}

func (op stockAdjustmentOperation) kind() string {
	switch op {
	case stockWaste:
		return "merma"
	case stockPhysicalCount:
		return "conteo"
	default:
		return ""
	}
}

func (op stockAdjustmentOperation) valid(product, qty int64) bool {
	return op.kind() != "" && product > 0 && qty >= 0 && qty <= stockQuantityMax && (op != stockWaste || qty > 0)
}

func (p stockAdjustmentProposal) movementID() string {
	// Distinct fixed context is incorporated into the existing scoped hash
	// derivation; input never reaches the native write as a request key.
	return stockReceiptMovementID(p.Scope, "adjustment.v1:"+p.Operation.kind()+":"+p.RequestID)
}

func stockAdjustmentSnapshot(ctx context.Context, tx *sql.Tx, s stockReceiptScope, op stockAdjustmentOperation, product, qty int64) (stockAdjustmentPreview, error) {
	p := stockAdjustmentPreview{ProductID: product, Quantity: qty}
	base, err := readStockReceiptPreview(ctx, tx, s, stockReceiptInput{ProductID: product})
	if err != nil {
		return p, err
	}
	p.ProductName, p.Revision, p.StockMinimum = base.ProductName, base.ProductRevision, base.StockMinimum
	err = tx.QueryRowContext(ctx, `SELECT coalesce(max(id),0),coalesce(CAST(sum(delta) AS BIGINT),0)
   FROM stock_movements WHERE outlet_id=$1 AND product_id=$2`, s.Outlet, product).Scan(&p.LatestMovement, &p.Before)
	if err != nil {
		return p, err
	}
	switch op {
	case stockWaste:
		if p.Before < math.MinInt64+qty {
			return p, stockAdjustmentStale
		}
		p.Delta, p.After = -qty, p.Before-qty
	case stockPhysicalCount:
		if p.Before < qty-math.MaxInt64 {
			return p, stockAdjustmentStale
		}
		p.Delta, p.After = qty-p.Before, qty
	default:
		return p, badInput
	}
	return p, nil
}

func (a *app) prepareStockWaste(ctx context.Context, tx *sql.Tx, s stockReceiptScope, input stockWasteInput, key string) (stockAdjustmentProposal, error) {
	return a.prepareStockAdjustment(ctx, tx, s, stockWaste, input.ProductID, input.Quantity, key)
}

func (a *app) prepareStockPhysicalCount(ctx context.Context, tx *sql.Tx, s stockReceiptScope, input stockPhysicalCountInput, key string) (stockAdjustmentProposal, error) {
	if input.Quantity == nil {
		return stockAdjustmentProposal{}, badInput
	}
	return a.prepareStockAdjustment(ctx, tx, s, stockPhysicalCount, input.ProductID, *input.Quantity, key)
}

func (a *app) prepareStockAdjustment(ctx context.Context, tx *sql.Tx, s stockReceiptScope, op stockAdjustmentOperation, product, qty int64, key string) (stockAdjustmentProposal, error) {
	if !posRequestID.MatchString(key) || !op.valid(product, qty) {
		return stockAdjustmentProposal{}, badInput
	}
	if err := a.lockStockReceiptScope(ctx, tx, s); err != nil {
		return stockAdjustmentProposal{}, err
	}
	p, err := stockAdjustmentSnapshot(ctx, tx, s, op, product, qty)
	if err != nil {
		return stockAdjustmentProposal{}, err
	}
	return stockAdjustmentProposal{op, s, key, p}, nil
}

// Caller checks explicit same-actor browser approval and proposal lifetime,
// and persists its terminal receipt atomically in this same transaction.
func (a *app) applyStockAdjustment(r *http.Request, tx *sql.Tx, u user, s stockReceiptScope, p stockAdjustmentProposal, confirmed [32]byte, now time.Time) (stockMovement, error) {
	var empty stockMovement
	if u.id != s.Actor || u.id == 0 {
		return empty, unauthenticated
	}
	d := p.digest()
	if p.Scope != s || subtle.ConstantTimeCompare(d[:], confirmed[:]) != 1 || !posRequestID.MatchString(p.RequestID) || !p.Operation.valid(p.Preview.ProductID, p.Preview.Quantity) {
		return empty, stockAdjustmentStale
	}
	ctx := r.Context()
	if err := a.lockStockReceiptScope(ctx, tx, s); err != nil {
		return empty, err
	}
	active, err := currentUser(nil, r, tx)
	if err != nil {
		return empty, err
	}
	if active.id != u.id {
		return empty, unauthenticated
	}
	var prior stockMovement
	var product, actor int64
	movementID := p.movementID()
	err = tx.QueryRowContext(ctx, `SELECT id,product_id,actor_id,kind,quantity,delta,recorded_at FROM stock_movements WHERE outlet_id=$1 AND request_id=$2`, s.Outlet, movementID).
		Scan(&prior.ID, &product, &actor, &prior.Kind, &prior.Quantity, &prior.Delta, &prior.RecordedAt)
	if err == nil {
		if product != p.Preview.ProductID || actor != s.Actor || prior.Kind != p.Operation.kind() || prior.Quantity != p.Preview.Quantity || prior.Delta != p.Preview.Delta {
			return empty, stockAdjustmentStale
		}
		// Native request IDs outlive proposal retention. A new preparation
		// reusing an old UUID must not adopt its earlier movement.
		var head, balance int64
		err = tx.QueryRowContext(ctx, `SELECT coalesce(max(id),0),coalesce(CAST(sum(delta) AS BIGINT),0)
            FROM stock_movements WHERE outlet_id=$1 AND product_id=$2 AND id<$3`,
			s.Outlet, product, prior.ID).Scan(&head, &balance)
		if err != nil {
			return empty, err
		}
		if head != p.Preview.LatestMovement || balance != p.Preview.Before {
			return empty, stockAdjustmentStale
		}
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	fresh, err := stockAdjustmentSnapshot(ctx, tx, s, p.Operation, p.Preview.ProductID, p.Preview.Quantity)
	if err == stockNotTracked || (err == nil && fresh != p.Preview) {
		return empty, stockAdjustmentStale
	}
	if err != nil {
		return empty, err
	}
	_, row, err := recordStockMovement(ctx, tx, s.Outlet, fresh.ProductID, s.Actor, stockInput{RequestID: movementID, Kind: p.Operation.kind(), Quantity: &fresh.Quantity}, now)
	if err != nil {
		return empty, err
	}
	result := row.(stockMovement)
	if result.Delta != fresh.Delta {
		return empty, stockAdjustmentStale
	}
	return result, nil
}
