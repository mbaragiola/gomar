package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const deliveryReceived = "received"
const deliveryAccepted = "accepted"
const deliveryCancelled = "cancelled"

var deliveryExternalID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$`)
var errDeliveryInvalid = errors.New("invalid normalized delivery fixture")
var errDeliveryUnknownSource = errors.New("no active delivery store mapping")
var errDeliveryConflict = errors.New("delivery event conflicts with its original mapping or payload")

type deliveryItem struct {
	Name     string `json:"nombre"`
	Quantity int64  `json:"cantidad"`
}

type deliveryOrder struct {
	ID              int64          `json:"id"`
	Provider        string         `json:"proveedor"`
	ExternalOrderID string         `json:"pedido_externo"`
	Status          string         `json:"estado"`
	Items           []deliveryItem `json:"items"`
	Received        string         `json:"recibido_en"`
	Updated         string         `json:"actualizado_en"`
}

// deliveryEvent is a trusted, normalized store input, not an inbound HTTP
// payload. No user-controlled route or credential invokes it in this release.
type deliveryEvent struct {
	Provider, StoreID, OrderID, EventID, Kind string
	Items                                     []deliveryItem
}

type deliveryReceipt struct {
	OrderID int64
	Seen    bool
}

func normalizeDelivery(event deliveryEvent) (string, string, error) {
	if (event.Provider != "pedidosya" && event.Provider != "rappi" && event.Provider != "uber_eats") || !deliveryExternalID.MatchString(event.StoreID) ||
		!deliveryExternalID.MatchString(event.OrderID) || !deliveryExternalID.MatchString(event.EventID) {
		return "", "", errDeliveryInvalid
	}
	items := make([]deliveryItem, 0, len(event.Items))
	switch event.Kind {
	case deliveryAccepted, deliveryReceived:
		if (event.Provider == "rappi") != (event.Kind == deliveryReceived) {
			return "", "", errDeliveryInvalid
		}
		if len(event.Items) < 1 || len(event.Items) > 50 {
			return "", "", errDeliveryInvalid
		}
		for _, item := range event.Items {
			name := strings.TrimSpace(item.Name)
			if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 120 || strings.IndexFunc(name, unicode.IsControl) >= 0 ||
				item.Quantity < 1 || item.Quantity > 999 {
				return "", "", errDeliveryInvalid
			}
			items = append(items, deliveryItem{name, item.Quantity})
		}
	case deliveryCancelled:
		if len(event.Items) != 0 {
			return "", "", errDeliveryInvalid
		}
	default:
		return "", "", errDeliveryInvalid
	}
	payload, err := json.Marshal(items)
	if err != nil {
		return "", "", err
	}
	pin := sha256.Sum256(payload)
	return string(payload), fmt.Sprintf("%x", pin), nil
}

func ingestDelivery(ctx context.Context, db *sql.DB, event deliveryEvent, now time.Time) (deliveryReceipt, error) {
	items, digest, err := normalizeDelivery(event)
	if err != nil {
		return deliveryReceipt{}, err
	}
	if now.IsZero() {
		return deliveryReceipt{}, errDeliveryInvalid
	}
	hasItems := event.Kind == deliveryAccepted || event.Kind == deliveryReceived
	stamp := now.UTC().Format("2006-01-02T15:04:05.000000Z")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return deliveryReceipt{}, err
	}
	defer tx.Rollback()
	var outlet int64
	err = tx.QueryRowContext(ctx, `SELECT s.outlet_id FROM delivery_sources s JOIN outlets o ON o.id=s.outlet_id AND o.active=TRUE
		JOIN entities e ON e.id=o.entity_id AND e.active=TRUE WHERE s.provider=$1 AND s.external_store_id=$2`, event.Provider, event.StoreID).Scan(&outlet)
	if errors.Is(err, sql.ErrNoRows) {
		return deliveryReceipt{}, errDeliveryUnknownSource
	}
	if err != nil {
		return deliveryReceipt{}, err
	}
	var observedOrderID int64
	var observedExternalID, observedKind, observedDigest string
	err = tx.QueryRowContext(ctx, `SELECT e.order_id,o.external_order_id,e.event_kind,e.digest
		FROM delivery_order_events e JOIN delivery_orders o ON o.id=e.order_id AND o.provider=e.provider AND o.external_store_id=e.external_store_id
		WHERE e.provider=$1 AND e.external_store_id=$2 AND e.external_event_id=$3`, event.Provider, event.StoreID, event.EventID).
		Scan(&observedOrderID, &observedExternalID, &observedKind, &observedDigest)
	if err == nil {
		if observedExternalID != event.OrderID || observedKind != event.Kind || observedDigest != digest {
			return deliveryReceipt{}, errDeliveryConflict
		}
		if err := tx.Commit(); err != nil {
			return deliveryReceipt{}, err
		}
		return deliveryReceipt{observedOrderID, true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return deliveryReceipt{}, err
	}
	var id int64
	var priorItems sql.NullString
	var cancelled bool
	err = tx.QueryRowContext(ctx, `SELECT id,accepted_items,cancelled FROM delivery_orders
		WHERE provider=$1 AND external_store_id=$2 AND external_order_id=$3 AND outlet_id=$4`, event.Provider, event.StoreID, event.OrderID, outlet).
		Scan(&id, &priorItems, &cancelled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var accepted any
		if hasItems {
			accepted = items
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO delivery_orders
			(provider,external_store_id,external_order_id,outlet_id,accepted_items,cancelled,received_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, event.Provider, event.StoreID, event.OrderID, outlet, accepted, event.Kind == deliveryCancelled, stamp, stamp).Scan(&id)
	case err != nil:
		return deliveryReceipt{}, err
	case event.Kind == deliveryCancelled && !cancelled:
		_, err = tx.ExecContext(ctx, `UPDATE delivery_orders SET cancelled=TRUE,updated_at=CASE WHEN updated_at<$1 THEN $1 ELSE updated_at END WHERE id=$2`, stamp, id)
	case hasItems && !priorItems.Valid:
		// A late dispatch may fill the sparse cancellation, never reopen it.
		_, err = tx.ExecContext(ctx, `UPDATE delivery_orders SET accepted_items=$1,updated_at=CASE WHEN updated_at<$2 THEN $2 ELSE updated_at END WHERE id=$3`, items, stamp, id)
	case hasItems && priorItems.String != items:
		return deliveryReceipt{}, errDeliveryConflict
	}
	if err != nil {
		return deliveryReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_order_events
		(provider,external_store_id,external_event_id,order_id,event_kind,digest,recorded_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, event.Provider, event.StoreID, event.EventID, id, event.Kind, digest, stamp); err != nil {
		return deliveryReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return deliveryReceipt{}, err
	}
	return deliveryReceipt{id, false}, nil
}

func (a *app) registerDelivery(add func(string, string, http.HandlerFunc)) {
	if a.enableDelivery {
		add("/api/v1/entities/{entity}/outlets/{outlet}/delivery/orders", http.MethodGet, a.scoped("read_business_data", listDeliveryOrders))
	}
}

func listDeliveryOrders(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	outlet, err := pathID(r, "outlet")
	if err != nil {
		return 0, nil, err
	}
	if err := requireSelectedDocumentOutlet(tx, m, outlet); err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,provider,external_order_id,cancelled,accepted_items,received_at,updated_at
		FROM delivery_orders WHERE outlet_id=$1 ORDER BY updated_at DESC,id DESC LIMIT 100`, outlet)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	orders := []deliveryOrder{}
	for rows.Next() {
		var order deliveryOrder
		var cancelled bool
		var raw sql.NullString
		if err := rows.Scan(&order.ID, &order.Provider, &order.ExternalOrderID, &cancelled, &raw, &order.Received, &order.Updated); err != nil {
			return 0, nil, err
		}
		order.Status = "aceptado"
		if order.Provider == "rappi" {
			order.Status = "recibido"
		}
		if cancelled {
			order.Status = "cancelado"
		}
		order.Items = []deliveryItem{}
		if raw.Valid {
			if err := json.Unmarshal([]byte(raw.String), &order.Items); err != nil {
				return 0, nil, err
			}
		}
		orders = append(orders, order)
	}
	return 200, struct {
		Orders []deliveryOrder `json:"pedidos"`
	}{orders}, rows.Err()
}
