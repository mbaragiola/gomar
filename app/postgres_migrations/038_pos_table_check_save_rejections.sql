-- A rejected transfer UUID cannot commit on a delayed retry. This history
-- contains no product lines and does not reserve or release a table.
CREATE TABLE pos_table_check_save_rejections (
    outlet_id BIGINT NOT NULL REFERENCES outlets(id) ON DELETE RESTRICT,
    actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    request_id TEXT COLLATE "C" NOT NULL CHECK(length(request_id)=36),
    request_fingerprint BYTEA NOT NULL CHECK(octet_length(request_fingerprint)=32),
    table_number BIGINT NOT NULL CHECK(table_number BETWEEN 1 AND 9999),
    reason TEXT NOT NULL CHECK(reason IN ('mesa_ocupada','catalogo_cambio')),
    PRIMARY KEY(outlet_id,actor_id,request_id)
);
CREATE TRIGGER pos_table_check_save_rejections_immutable BEFORE UPDATE OR DELETE ON pos_table_check_save_rejections
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
