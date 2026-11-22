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
	if err := c.getWorkspace(ctx, entity, outlet, path, q, &result); err != nil {
		return PayableMovements{}, err
	}
	if result.PayableID != payable || !validFinanceCurrency(result.Currency) ||
		result.Movements == nil || len(result.Movements) > workspacePageSize {
		return PayableMovements{}, invalidResponse
	}
	if result.NextCursor != "" {
		next, err := ParsePayableMovementCursor(payable, result.NextCursor)
		if err != nil || next <= after || len(result.Movements) != workspacePageSize {
			return PayableMovements{}, invalidResponse
		}
	}
	const maxMinor = 99999999999999 // Matches the native payable/ledger limits.
	for _, row := range result.Movements {
		if row.Date != nil && !accountingDate(*row.Date) || row.BalanceMinor < 0 || row.BalanceMinor > maxMinor ||
			row.DeltaMinor < -maxMinor || row.DeltaMinor > maxMinor {
			return PayableMovements{}, invalidResponse
		}
		switch row.Kind {
		case "post", "reversal":
			if row.DeltaMinor <= 0 {
				return PayableMovements{}, invalidResponse
			}
		case "payment":
			if row.DeltaMinor >= 0 {
				return PayableMovements{}, invalidResponse
			}
		default:
			return PayableMovements{}, invalidResponse
		}
	}
	return result, nil
}
