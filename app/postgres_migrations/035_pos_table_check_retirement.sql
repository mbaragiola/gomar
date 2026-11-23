-- Keep saved checks immutable. A separate slot points to the check that is
-- currently open for a table; a retirement records why that slot was freed.
ALTER TABLE pos_table_checks ADD UNIQUE(id,outlet_id,table_number);
CREATE TABLE pos_table_check_slots (
    check_id BIGINT NOT NULL UNIQUE,
    outlet_id BIGINT NOT NULL,
    table_number BIGINT NOT NULL,
    PRIMARY KEY(outlet_id,table_number),
    FOREIGN KEY(check_id,outlet_id,table_number)
        REFERENCES pos_table_checks(id,outlet_id,table_number) ON DELETE RESTRICT
);
CREATE TABLE pos_table_check_retirements (
    check_id BIGINT PRIMARY KEY,
    outlet_id BIGINT NOT NULL,
    table_number BIGINT NOT NULL,
    actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    reason TEXT NOT NULL CHECK(reason IN ('saved_in_error','cancelled')),
    retired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY(check_id,outlet_id,table_number)
        REFERENCES pos_table_checks(id,outlet_id,table_number) ON DELETE RESTRICT
);
CREATE TRIGGER pos_table_check_retirements_immutable BEFORE UPDATE OR DELETE ON pos_table_check_retirements
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
INSERT INTO pos_table_check_slots(check_id,outlet_id,table_number)
    SELECT id,outlet_id,table_number FROM pos_table_checks;
ALTER TABLE pos_table_checks DROP CONSTRAINT pos_table_checks_outlet_id_table_number_key;
