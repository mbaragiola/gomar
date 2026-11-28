CREATE TABLE gomar.tenant_sales_exports (
    actor_id BIGINT NOT NULL REFERENCES gomar.users(id),
    request_id TEXT COLLATE "C" NOT NULL CHECK (length(request_id)=36),
    entity_id BIGINT NOT NULL,
    outlet_id BIGINT NOT NULL,
    from_day DATE NOT NULL,
    through_day DATE NOT NULL CHECK (through_day >= from_day AND through_day - from_day < 31),
    sale_ids JSONB NOT NULL CHECK (jsonb_typeof(sale_ids)='array' AND jsonb_array_length(sale_ids)<=1000),
    digest BYTEA NOT NULL CHECK (octet_length(digest)=32),
    prepared_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(actor_id,request_id),
    FOREIGN KEY(outlet_id,entity_id) REFERENCES gomar.outlets(id,entity_id)
);
CREATE TABLE gomar.tenant_sales_export_acknowledgments (
    actor_id BIGINT NOT NULL,
    request_id TEXT COLLATE "C" NOT NULL,
    acknowledged_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(actor_id,request_id),
    FOREIGN KEY(actor_id,request_id) REFERENCES gomar.tenant_sales_exports(actor_id,request_id)
);
CREATE TRIGGER tenant_sales_exports_immutable BEFORE UPDATE OR DELETE ON gomar.tenant_sales_exports
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER tenant_sales_export_acknowledgments_immutable BEFORE UPDATE OR DELETE ON gomar.tenant_sales_export_acknowledgments
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
