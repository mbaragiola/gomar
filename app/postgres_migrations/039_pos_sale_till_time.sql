-- An optional, unverified clock claim accepted only at first registration.
-- Existing sales stay NULL. The sale's existing immutable trigger also
-- protects this column. Store the exact UTC text, including its precision.
ALTER TABLE pos_sales ADD COLUMN till_reported_at_utc TEXT COLLATE "C"
    CHECK (till_reported_at_utc IS NULL OR (
        left(till_reported_at_utc,4) <> '0000'
        AND till_reported_at_utc ~ '^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]([.][0-9]{1,9})?Z$'
    ));
