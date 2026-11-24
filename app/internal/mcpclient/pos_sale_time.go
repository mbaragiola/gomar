package mcpclient

import (
	"context"
	"errors"

	"github.com/mbaragiola/gomar/app/internal/postime"
)

func (c *Client) ReadPOSSaleTime(ctx context.Context, entity, outlet int64, requestID string) (postime.Evidence, error) {
	if !postime.ValidRequestID(requestID) {
		return postime.Evidence{}, errors.New("indicá la referencia exacta de la venta de Caja")
	}
	var result postime.Evidence
	if err := c.getWorkspace(ctx, entity, outlet, "/pos-sale-times/"+requestID, nil, &result); err != nil {
		return postime.Evidence{}, err
	}
	if _, ok := postime.ParseUTC(result.RegisteredAt); !ok ||
		result.RequestID != requestID || result.HandoffURL != postime.Handoff(entity, outlet, requestID) {
		return postime.Evidence{}, invalidResponse
	}
	if result.TillTimeStatus == postime.NotReported && result.TillReportedAt == "" {
		return result, nil
	}
	if _, ok := postime.ParseUTC(result.TillReportedAt); !ok ||
		result.TillTimeStatus != postime.Unverified && result.TillTimeStatus != postime.Review {
		return postime.Evidence{}, invalidResponse
	}
	return result, nil
}
