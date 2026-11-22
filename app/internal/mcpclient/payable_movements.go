package mcpclient

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type PayableMovement struct {
	Kind         string  `json:"kind"`
	Date         *string `json:"date,omitempty"`
	DeltaMinor   int64   `json:"delta_minor"`
	BalanceMinor int64   `json:"balance_minor"`
}

type PayableMovements struct {
	PayableID  int64             `json:"payable_id"`
	Currency   string            `json:"currency"`
	Movements  []PayableMovement `json:"movements"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func ParsePayableMovementCursor(payable int64, cursor string) (int64, error) {
	bound, last, ok := strings.Cut(cursor, ".")
	id, err := strconv.ParseInt(last, 10, 64)
	if !ok || payable <= 0 || bound != strconv.FormatInt(payable, 10) ||
		err != nil || id <= 0 || strconv.FormatInt(id, 10) != last {
		return 0, errors.New("usá next_cursor de la misma cuenta a pagar")
	}
	return id, nil
}

func (c *Client) ListPayableMovements(ctx context.Context, entity, outlet, payable int64, cursor *string) (PayableMovements, error) {
	var result PayableMovements
	if payable <= 0 {
		return result, errors.New("indicá el payable_id devuelto por gomar_list_payables")
	}
	var q url.Values
	var after int64
	if cursor != nil {
		var err error
		after, err = ParsePayableMovementCursor(payable, *cursor)
		if err != nil {
			return result, err
		}
		q = url.Values{"cursor": {*cursor}}
	}
	path := "/payable-records/" + strconv.FormatInt(payable, 10) + "/movements"
	var wire struct {
		PayableID int64  `json:"payable_id"`
		Currency  string `json:"currency"`
		Movements []struct {
			Kind         string  `json:"kind"`
			Date         *string `json:"date"`
			DeltaMinor   *int64  `json:"delta_minor"`
			BalanceMinor *int64  `json:"balance_minor"`
		} `json:"movements"`
		NextCursor string `json:"next_cursor"`
	}
	if err := c.getWorkspace(ctx, entity, outlet, path, q, &wire); err != nil {
		return PayableMovements{}, err
	}
	if wire.PayableID != payable || !validFinanceCurrency(wire.Currency) ||
		wire.Movements == nil || len(wire.Movements) > workspacePageSize {
		return PayableMovements{}, invalidResponse
	}
	if wire.NextCursor != "" {
		next, err := ParsePayableMovementCursor(payable, wire.NextCursor)
		if err != nil || next <= after || len(wire.Movements) != workspacePageSize {
			return PayableMovements{}, invalidResponse
		}
	}
	result = PayableMovements{PayableID: wire.PayableID, Currency: wire.Currency, NextCursor: wire.NextCursor, Movements: make([]PayableMovement, 0, len(wire.Movements))}
	const maxMinor = 99999999999999 // Matches the native payable/ledger limits.
	for _, row := range wire.Movements {
		if row.DeltaMinor == nil || row.BalanceMinor == nil {
			return PayableMovements{}, invalidResponse
		}
		delta, balance := *row.DeltaMinor, *row.BalanceMinor
		if row.Date != nil && !accountingDate(*row.Date) || balance < 0 || balance > maxMinor ||
			delta < -maxMinor || delta > maxMinor {
			return PayableMovements{}, invalidResponse
		}
		switch row.Kind {
		case "post", "reversal":
			if delta <= 0 {
				return PayableMovements{}, invalidResponse
			}
		case "payment":
			if delta >= 0 {
				return PayableMovements{}, invalidResponse
			}
		default:
			return PayableMovements{}, invalidResponse
		}
		result.Movements = append(result.Movements, PayableMovement{Kind: row.Kind, Date: row.Date, DeltaMinor: delta, BalanceMinor: balance})
	}
	return result, nil
}
