-- Only the explicit table workflow may insert a mixed sale. The ordinary
-- offline endpoint still validates its original six methods.
ALTER TABLE pos_sales DROP CONSTRAINT pos_sales_payment_method_check;
ALTER TABLE pos_sales ADD CHECK(payment_method IN ('debito','credito','qr_modo','qr_mercado_pago','transferencia','efectivo','mixto'));

CREATE TABLE pos_table_finalizations (
    check_id BIGINT PRIMARY KEY,
    outlet_id BIGINT NOT NULL,
    table_number BIGINT NOT NULL,
    sale_id BIGINT NOT NULL UNIQUE,
    tender_revision BIGINT NOT NULL,
    revision_kind TEXT GENERATED ALWAYS AS ('record'::text) STORED,
    UNIQUE(check_id,outlet_id),
    FOREIGN KEY(check_id,outlet_id,table_number)
        REFERENCES pos_table_checks(id,outlet_id,table_number) ON DELETE RESTRICT,
    FOREIGN KEY(sale_id,outlet_id) REFERENCES pos_sales(id,outlet_id) ON DELETE RESTRICT,
    FOREIGN KEY(tender_revision,outlet_id,check_id,revision_kind)
        REFERENCES pos_table_tender_events(id,outlet_id,check_id,kind) ON DELETE RESTRICT
);
CREATE TABLE pos_table_finalization_legs (
    check_id BIGINT NOT NULL,
    outlet_id BIGINT NOT NULL,
    tender_id BIGINT NOT NULL,
    tender_kind TEXT GENERATED ALWAYS AS ('record'::text) STORED,
    PRIMARY KEY(check_id,tender_id),
    FOREIGN KEY(check_id,outlet_id) REFERENCES pos_table_finalizations(check_id,outlet_id) ON DELETE RESTRICT,
    FOREIGN KEY(tender_id,outlet_id,check_id,tender_kind)
        REFERENCES pos_table_tender_events(id,outlet_id,check_id,kind) ON DELETE RESTRICT
);
CREATE TRIGGER pos_table_finalizations_immutable BEFORE UPDATE OR DELETE ON pos_table_finalizations
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER pos_table_finalization_legs_immutable BEFORE UPDATE OR DELETE ON pos_table_finalization_legs
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
