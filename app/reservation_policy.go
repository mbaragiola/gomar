package main

import (
	"context"
	"database/sql"
)

func reservationSubmittedPolicy(ctx context.Context, tx *sql.Tx, id string) (*string, error) {
	var text string
	if err := tx.QueryRowContext(ctx, "SELECT policy FROM reservation_request_policies WHERE request_id=$1", id).Scan(&text); err != nil {
		return nil, err
	}
	return &text, nil
}
