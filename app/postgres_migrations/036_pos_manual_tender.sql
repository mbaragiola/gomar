-- Outside-payment notes only; no sale, charge, refund or fiscal instruction.
CREATE TABLE pos_table_tender_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    outlet_id BIGINT NOT NULL,
    check_id BIGINT NOT NULL,
    actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    request_id TEXT COLLATE "C" NOT NULL CHECK(request_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    kind TEXT NOT NULL,
    amount_minor BIGINT,
    method TEXT,
    target_id BIGINT,
    target_kind TEXT GENERATED ALWAYS AS (CASE WHEN target_id IS NOT NULL THEN 'record' END) STORED,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK(
        (kind='record' AND amount_minor IS NOT NULL AND amount_minor BETWEEN 1 AND 10000000000
         AND method IS NOT NULL AND method IN ('debito','credito','qr_modo','qr_mercado_pago','transferencia','efectivo')
         AND target_id IS NULL AND reason IS NULL)
        OR (kind='void' AND amount_minor IS NULL AND method IS NULL AND target_id IS NOT NULL AND target_id < id
            AND reason IS NOT NULL AND reason IN ('entrada_incorrecta','medio_incorrecto','importe_incorrecto'))
    ),
    UNIQUE(outlet_id,request_id),
    UNIQUE(id,outlet_id,check_id,kind),
    FOREIGN KEY(check_id,outlet_id) REFERENCES pos_table_checks(id,outlet_id) ON DELETE RESTRICT,
    FOREIGN KEY(target_id,outlet_id,check_id,target_kind)
        REFERENCES pos_table_tender_events(id,outlet_id,check_id,kind) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX pos_table_tender_one_void ON pos_table_tender_events(target_id) WHERE target_id IS NOT NULL;
CREATE INDEX pos_table_tender_check_history ON pos_table_tender_events(outlet_id,check_id,id);
CREATE TRIGGER pos_table_tender_events_immutable BEFORE UPDATE OR DELETE ON pos_table_tender_events
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
