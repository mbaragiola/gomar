-- Preserve final SQLite partial/ordered indexes and write guards.
CREATE INDEX visible_documents ON documents(entity_id,outlet_id,issue_date DESC,id DESC) WHERE deleted_at IS NULL;

CREATE INDEX ap_payables_scope ON ap_payables(entity_id, due_date_snapshot, id);

CREATE INDEX document_attachment_history ON document_attachments(document_id, id);

CREATE UNIQUE INDEX one_document_creation ON document_audit(document_id) WHERE action = 'create';

CREATE INDEX document_audit_history ON document_audit(document_id, occurred_at, id);

CREATE INDEX reconciliation_read_entity ON reconciliation_read_runs(entity_id,id);

CREATE UNIQUE INDEX reconciliation_detail_event_operation_key
ON reconciliation_detail_events(run_id,operation_key) WHERE operation_key IS NOT NULL;

CREATE UNIQUE INDEX reconciliation_source_operation_key
ON reconciliation_read_sources(run_id,operation_key) WHERE operation_key IS NOT NULL;

CREATE INDEX reconciliation_review_rejections_group
ON "reconciliation_review_decisions"(run_id,group_ordinal,payment_row_id,id);

CREATE UNIQUE INDEX reconciliation_confirmed_payment_once
ON reconciliation_review_decisions(payment_row_id) WHERE decision='confirmed';

CREATE UNIQUE INDEX reconciliation_confirmed_cash_once
ON reconciliation_review_decisions(cash_flow_row_id) WHERE decision='confirmed';

CREATE INDEX admin_outlet_history_item ON admin_outlet_history(outlet_id, occurred_at, id);

CREATE INDEX admin_supplier_history_item ON admin_supplier_history(supplier_id, occurred_at, id);

CREATE INDEX ap_payments_scope ON ap_payments(entity_id, created_at DESC, id DESC);

CREATE INDEX ap_ledger_payable ON ap_ledger(payable_id, id);

CREATE FUNCTION gomar.gomar_guard_payable_document() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $guard$
BEGIN
    IF EXISTS (SELECT 1 FROM gomar.ap_payables WHERE source_document_id=OLD.id) THEN
        RAISE EXCEPTION 'posted payable source is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$guard$;
CREATE TRIGGER ap_document_no_update BEFORE UPDATE ON documents
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_guard_payable_document();

CREATE FUNCTION gomar.gomar_guard_supplier_cuit() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $guard$
BEGIN
    IF NEW.cuit IS DISTINCT FROM OLD.cuit AND EXISTS
          (SELECT 1 FROM gomar.ap_payables WHERE supplier_id=OLD.id) THEN
        RAISE EXCEPTION 'supplier CUIT has payable history' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$guard$;
CREATE TRIGGER ap_supplier_cuit_no_update BEFORE UPDATE OF cuit ON suppliers
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_guard_supplier_cuit();

CREATE TRIGGER ap_payable_snapshot_no_update BEFORE UPDATE OF
    id,entity_id,outlet_id,supplier_id,source_document_id,currency,original_minor,
    supplier_name_snapshot,supplier_cuit_snapshot,document_reference_snapshot,issue_date_snapshot,
    due_date_snapshot,posted_by_id,posted_at ON ap_payables
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER ap_payable_no_delete BEFORE DELETE ON ap_payables
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();

CREATE TRIGGER ap_payment_details_no_update BEFORE UPDATE OF
    id,entity_id,outlet_id,supplier_id,currency,total_minor,method,reference,
    kind,reversal_of_id,reversal_reason,created_by_id,created_at,post_key ON ap_payments
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER ap_payment_no_delete BEFORE DELETE ON ap_payments
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();

CREATE FUNCTION gomar.gomar_guard_payment_transition() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $guard$
BEGIN
    IF NOT (
        (OLD.status='draft' AND NEW.status='posted') OR
        (OLD.status='posted' AND OLD.kind='payment' AND NEW.status='reversed'
         AND NEW.posted_by_id IS NOT DISTINCT FROM OLD.posted_by_id
         AND NEW.posted_at IS NOT DISTINCT FROM OLD.posted_at)
    ) THEN
        RAISE EXCEPTION 'invalid payment transition' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$guard$;
CREATE TRIGGER ap_payment_transition BEFORE UPDATE OF status,posted_by_id,posted_at ON ap_payments
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_guard_payment_transition();

CREATE TRIGGER ap_allocations_immutable BEFORE UPDATE OR DELETE ON ap_allocations
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER ap_ledger_immutable BEFORE UPDATE OR DELETE ON ap_ledger
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER ap_financial_events_immutable BEFORE UPDATE OR DELETE ON ap_financial_events
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER ap_receipts_immutable BEFORE UPDATE OR DELETE ON ap_receipts
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER document_audit_immutable BEFORE UPDATE OR DELETE ON document_audit
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER admin_outlet_history_immutable BEFORE UPDATE OR DELETE ON admin_outlet_history
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER admin_supplier_history_immutable BEFORE UPDATE OR DELETE ON admin_supplier_history
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER admin_legal_entity_history_immutable BEFORE UPDATE OR DELETE ON admin_legal_entity_history
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER ocr_consent_events_immutable BEFORE UPDATE OR DELETE ON ocr_consent_events
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER reconciliation_detail_events_immutable BEFORE UPDATE OR DELETE ON reconciliation_detail_events
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER reconciliation_native_origin_immutable BEFORE UPDATE OR DELETE ON reconciliation_native_v1_runs
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();

CREATE FUNCTION gomar.gomar_set_reconciliation_source_highwater() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $source$
BEGIN
    UPDATE gomar.reconciliation_source_highwater
        SET source_id=GREATEST(source_id,NEW.id) WHERE singleton=1;
    RETURN NEW;
END;
$source$;
CREATE TRIGGER reconciliation_source_highwater_after_insert AFTER INSERT ON reconciliation_read_sources
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_set_reconciliation_source_highwater();

CREATE FUNCTION gomar.gomar_set_reconciliation_row_highwater() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $row$
BEGIN
    UPDATE gomar.reconciliation_source_highwater
        SET row_id=GREATEST(row_id,NEW.id) WHERE singleton=1;
    RETURN NEW;
END;
$row$;
CREATE TRIGGER reconciliation_row_highwater_after_insert AFTER INSERT ON reconciliation_detail_rows
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_set_reconciliation_row_highwater();
