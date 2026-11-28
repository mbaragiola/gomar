package mcpclient

import (
	"context"
	"errors"
	"net/url"
	"strconv"

	"github.com/mbaragiola/gomar/app/internal/documentref"
)

const RemovedDraftPageSize = 30

type RemovedDraft struct {
	ID        int64  `json:"id"`
	Revision  int64  `json:"revision"`
	RemovedAt string `json:"removed_at"`
	documentref.Reference
	HandoffURL string `json:"handoff_url"`
}

type RemovedDraftPage struct {
	Items  []RemovedDraft `json:"items"`
	Before *int64         `json:"next_before_id,omitempty"`
}

func RemovedDraftHandoffURL(entity, outlet, document int64) string {
	const maxID = (1 << 53) - 1
	if entity <= 0 || outlet <= 0 || document <= 0 || entity > maxID || outlet > maxID || document > maxID {
		return ""
	}
	return "/#restaurar-borrador=" + strconv.FormatInt(entity, 10) + ":" + strconv.FormatInt(outlet, 10) + ":" + strconv.FormatInt(document, 10)
}

func (c *Client) RemovedDrafts(ctx context.Context, entity, outlet int64, before *int64) (RemovedDraftPage, error) {
	var query url.Values
	if before != nil {
		if *before <= 0 {
			return RemovedDraftPage{}, errors.New("usá el next_before_id de la página anterior u omití el cursor")
		}
		query = url.Values{"before_id": {strconv.FormatInt(*before, 10)}}
	}
	var page RemovedDraftPage
	if err := c.getWorkspace(ctx, entity, outlet, "/removed-drafts", query, &page); err != nil {
		return RemovedDraftPage{}, err
	}
	if page.Items == nil || len(page.Items) > RemovedDraftPageSize {
		return RemovedDraftPage{}, invalidResponse
	}
	last := before
	for _, item := range page.Items {
		expected := RemovedDraftHandoffURL(entity, outlet, item.ID)
		if expected == "" || item.HandoffURL != expected || item.Revision < 1 || (last != nil && item.ID >= *last) {
			return RemovedDraftPage{}, invalidResponse
		}
		last = &item.ID
	}
	if page.Before != nil && (len(page.Items) != RemovedDraftPageSize || *page.Before != *last) {
		return RemovedDraftPage{}, invalidResponse
	}
	return page, nil
}
