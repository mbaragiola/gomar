package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/mbaragiola/gomar/app/internal/tablecheck"
)

type POSTableCheck struct {
	TableNumber int64  `json:"table_number"`
	TotalMinor  int64  `json:"total_minor"`
	State       string `json:"state"`
	HandoffURL  string `json:"handoff_url"`
}

func (c *Client) ReadPOSTableCheck(ctx context.Context, entity, outlet, table int64) (POSTableCheck, error) {
	if table < 1 || table > tablecheck.MaxTable {
		return POSTableCheck{}, errors.New("indicá un número de mesa entre 1 y 9999")
	}
	var result POSTableCheck
	if err := c.getWorkspace(ctx, entity, outlet, "/pos-table-checks/"+strconv.FormatInt(table, 10), nil, &result); err != nil {
		return POSTableCheck{}, err
	}
	if result.TableNumber != table || result.TotalMinor < 1 || result.TotalMinor > tablecheck.MaxTotalMinor ||
		(result.State != "untendered" && result.State != "manual_records") || result.HandoffURL != fmt.Sprintf("/pos#entity=%d&outlet=%d&table=%d", entity, outlet, table) {
		return POSTableCheck{}, invalidResponse
	}
	return result, nil
}
