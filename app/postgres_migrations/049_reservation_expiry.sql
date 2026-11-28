ALTER TABLE reservation_requests DROP CONSTRAINT reservation_requests_status_check,
    ADD CONSTRAINT reservation_requests_status_check CHECK (status IN ('pending', 'confirmed', 'cancelled', 'expired'));

CREATE INDEX reservation_requests_pending_expiry ON reservation_requests (start_at, id)
    WHERE status = 'pending';
