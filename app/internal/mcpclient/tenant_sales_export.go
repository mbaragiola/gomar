package mcpclient

import (
	"context"
	"fmt"
)

type TenantSalesExportHandoff struct {
	HandoffURL string `json:"handoff_url"`
}

func TenantSalesExportURL(entity, outlet int64) string {
	return fmt.Sprintf("/exportaciones/ventas/%d/%d", entity, outlet)
}

func (c *Client) TenantSalesExport(ctx context.Context, entity, outlet int64) (TenantSalesExportHandoff, error) {
	var result TenantSalesExportHandoff
	if err := c.getWorkspace(ctx, entity, outlet, "/sales-export-handoff", nil, &result); err != nil {
		return TenantSalesExportHandoff{}, err
	}
	if result.HandoffURL != TenantSalesExportURL(entity, outlet) {
		return TenantSalesExportHandoff{}, invalidResponse
	}
	return result, nil
}
