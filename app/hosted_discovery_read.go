package main

import (
	"cmp"
	"context"
	"database/sql"
	"net/http"
	"slices"
	"time"
)

// Only explicitly injected in the disposable composition. This is not a
// general hosted-mode setting or authorization outside the documented reads.
type hostedDiscoveryReadFixture struct {
	now               func() time.Time
	observationSource *hostedEligibilitySource
}

func (f *hostedDiscoveryReadFixture) clock() time.Time {
	if f.now != nil {
		return f.now().UTC()
	}
	return time.Now().UTC()
}

// One set-based intersection for the documented native and MCP reads.
// Each statement supplies its own typed discovery_input, including the
// exact outlet for single-outlet reads.
// The latest epoch is selected FIRST: missing, disputed, negative or
// expired evidence cannot fall back to an older positive. Neither a
// current paid scope nor a membership can substitute for a branch grant.
// The native entity list alone keeps explicit self-host memberships as
// candidates, then applies its original capability projection unchanged.
const hostedScopeEligibilitySQL = `
granted AS (
    SELECT m.entity_id, c.class, o.id AS outlet_id, o.name, paid.business_ref
    FROM discovery_input i JOIN gomar.memberships m ON m.user_id=i.actor_id
    JOIN gomar.entities e ON e.id=m.entity_id AND e.active
    JOIN gomar.discovery_entity_class c ON c.entity_id=m.entity_id
    LEFT JOIN LATERAL (
        SELECT o.id,o.name FROM gomar.outlet_grants g
        JOIN gomar.outlets o ON o.id=g.outlet_id AND o.entity_id=m.entity_id AND o.active
        WHERE g.membership_id=m.id AND (i.outlet_id IS NULL OR o.id=i.outlet_id)
    ) o ON TRUE
    LEFT JOIN gomar.discovery_paid_outlet paid ON (paid.outlet_id,paid.entity_id)=(o.id,m.entity_id)
    WHERE m.active AND ((i.native_entity_list AND c.class='self_host')
      OR m.role IN ('administration','purchasing','accounting','read_only'))
      AND (i.entity_id IS NULL OR m.entity_id=i.entity_id)
), relevant AS (
    SELECT DISTINCT business_ref FROM granted WHERE business_ref IS NOT NULL
), newest AS (
    SELECT h.business_ref,MAX(h.epoch) AS epoch
    FROM gomar.discovery_scope_epoch h JOIN relevant b USING(business_ref)
    GROUP BY h.business_ref
), eligible AS (
    SELECT n.business_ref,MIN(h.verified_at) AS verified_at,MIN(h.valid_until) AS valid_until
    FROM newest n JOIN gomar.discovery_scope_epoch h ON (h.business_ref,h.epoch)=(n.business_ref,n.epoch)
    CROSS JOIN discovery_input i
    GROUP BY n.business_ref
    HAVING COUNT(*)=1 AND BOOL_AND(h.state='eligible' AND h.verified_at<=i.checked_at AND i.checked_at<h.valid_until
      AND h.branches=(SELECT array_agg(p.branch_ref ORDER BY p.branch_ref COLLATE "C")
                       FROM gomar.discovery_paid_outlet p WHERE p.business_ref=h.business_ref)
      AND NOT EXISTS (SELECT 1 FROM gomar.discovery_scope_epoch previous
          WHERE previous.business_ref=h.business_ref AND previous.epoch<h.epoch
          GROUP BY previous.epoch
          HAVING (COUNT(*)>1 AND MAX(previous.verified_at)>=h.verified_at)
              OR BOOL_OR(previous.state<>'eligible' AND previous.verified_at>=h.verified_at)))
), discovery_admitted AS (
    SELECT g.entity_id,g.outlet_id,COALESCE(g.name,'') AS name
    FROM granted g CROSS JOIN discovery_input i
    WHERE (g.class='self_host' AND (i.outlet_id IS NULL OR g.outlet_id IS NOT NULL))
       OR (g.class='paid_hosted' AND EXISTS (SELECT 1 FROM eligible e WHERE e.business_ref=g.business_ref))
)`

const hostedDiscoveryReadSQL = `
WITH discovery_input(actor_id,entity_id,outlet_id,checked_at,native_entity_list) AS (
    VALUES ($1::bigint,$2::bigint,$3::bigint,$4::timestamptz,$5::boolean)
), ` + hostedScopeEligibilitySQL + `
SELECT entity_id,outlet_id,name FROM discovery_admitted ORDER BY entity_id,name,outlet_id`

type discoveryReadQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (f *hostedDiscoveryReadFixture) discover(ctx context.Context, q discoveryReadQuerier, actor int64, entity, exactOutlet sql.NullInt64, nativeEntityList bool) (map[int64][]outlet, error) {
	rows, err := q.QueryContext(ctx, hostedDiscoveryReadSQL, actor, entity, exactOutlet, f.clock(), nativeEntityList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64][]outlet)
	for rows.Next() {
		var businessID int64
		var outletID sql.NullInt64
		var name string
		if err := rows.Scan(&businessID, &outletID, &name); err != nil {
			return nil, err
		}
		if _, ok := result[businessID]; !ok {
			result[businessID] = []outlet{}
		}
		if outletID.Valid {
			result[businessID] = append(result[businessID], outlet{ID: outletID.Int64, Name: name})
		}
	}
	return result, rows.Err()
}

func (a *app) listDiscoveryOutlets(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	if a.hostedDiscoveryRead == nil {
		return listOutlets(r, tx, m, body)
	}
	scopes, err := a.hostedDiscoveryRead.discover(r.Context(), tx, m.userID, sql.NullInt64{Int64: m.ID, Valid: true}, sql.NullInt64{}, false)
	if err != nil {
		return 0, nil, err
	}
	outlets, ok := scopes[m.ID]
	if !ok {
		return 0, nil, missingEntity
	}
	return http.StatusOK, outlets, nil
}

func (a *app) mcpDiscoveryEntities(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if a.hostedDiscoveryRead != nil {
		if r.URL.RawQuery != "" {
			return nil, badInput
		}
		if _, err := a.hostedMCPDiscovery(r, tx, s); err != nil {
			return nil, err
		}
	}
	return mcpEntities(r, tx, s)
}

func (a *app) mcpDiscoveryOutlets(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if a.hostedDiscoveryRead == nil {
		return mcpOutlets(r, tx, s)
	}
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	outlets, err := a.hostedMCPDiscovery(r, tx, s)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(outlets, func(a, b outlet) int { return cmp.Compare(a.ID, b.ID) })
	values := make([]mcpPlace, 0, len(outlets))
	for _, o := range outlets {
		values = append(values, mcpPlace{ID: o.ID, Name: o.Name})
	}
	return map[string]any{"outlets": values}, nil
}

func (a *app) hostedMCPDiscovery(r *http.Request, tx *sql.Tx, s mcpTokenScope) ([]outlet, error) {
	scopes, err := a.hostedDiscoveryRead.discover(r.Context(), tx, s.userID, sql.NullInt64{Int64: s.ID, Valid: true}, s.outlet, false)
	if err != nil {
		return nil, err
	}
	outlets, ok := scopes[s.ID]
	if !ok {
		return nil, missingEntity
	}
	return outlets, nil
}
