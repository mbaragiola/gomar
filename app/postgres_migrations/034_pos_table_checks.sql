-- First online-only snapshot of an untendered POS table check. This is not a
-- sale, payment, reservation, stock transaction, or fiscal document.
CREATE TABLE pos_table_checks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    outlet_id BIGINT NOT NULL REFERENCES outlets(id) ON DELETE RESTRICT,
    table_number BIGINT NOT NULL CHECK(table_number BETWEEN 1 AND 9999),
    actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    request_id TEXT COLLATE "C" NOT NULL CHECK(length(request_id)=36),
    request_fingerprint BYTEA NOT NULL CHECK(octet_length(request_fingerprint)=32),
    total_minor BIGINT NOT NULL CHECK(total_minor BETWEEN 1 AND 10000000000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(id,outlet_id),
    UNIQUE(outlet_id,table_number),
    UNIQUE(outlet_id,actor_id,request_id)
);

CREATE TABLE pos_table_check_items (
    check_id BIGINT NOT NULL,
    outlet_id BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    revision BIGINT NOT NULL,
    quantity BIGINT NOT NULL CHECK(quantity BETWEEN 1 AND 999),
    PRIMARY KEY(check_id,product_id),
    FOREIGN KEY(check_id,outlet_id) REFERENCES pos_table_checks(id,outlet_id) ON DELETE RESTRICT,
    FOREIGN KEY(product_id,outlet_id,revision)
        REFERENCES pos_product_versions(product_id,outlet_id,revision) ON DELETE RESTRICT
);
CREATE TRIGGER pos_table_checks_immutable BEFORE UPDATE OR DELETE ON pos_table_checks
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER pos_table_check_items_immutable BEFORE UPDATE OR DELETE ON pos_table_check_items
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
