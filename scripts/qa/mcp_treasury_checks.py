"""Synthetic treasury scope checks; drafts are fixtures and no payment is made."""
import re

from playwright.sync_api import expect


def treasury_seed(sql):
    sql("""
BEGIN;
SET search_path=gomar,public;
INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,
  number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at)
VALUES (16,1,2,11,'00000000011','invoice','A',12,347,'2026-11-01','ARS',800,'','reviewed',1,
  '2026-11-01T14:00:00Z','2026-11-01T14:00:00Z');
INSERT INTO ap_payables(id,entity_id,outlet_id,supplier_id,source_document_id,currency,original_minor,
  balance_minor,supplier_name_snapshot,supplier_cuit_snapshot,document_reference_snapshot,
  issue_date_snapshot,posted_by_id,posted_at)
SELECT 16,1,2,11,16,'ARS',800,800,'qa-private-supplier-11','00000000011','Factura QA Norte',
  '2026-11-01',id,'2026-11-01T14:00:00Z' FROM users WHERE username='qa-admin';
INSERT INTO ap_payments(id,entity_id,outlet_id,supplier_id,currency,total_minor,method,reference,
  status,kind,reversal_reason,created_by_id,created_at,post_key)
SELECT 55,1,1,11,'ARS',500,'cash','qa-private-payment-centro','draft','payment','',
  id,'2026-11-01T14:00:00Z','qa-private-key-centro' FROM users WHERE username='qa-admin'
UNION ALL
SELECT 56,1,2,11,'ARS',600,'bank_transfer','qa-private-payment-norte','draft','payment','',
  id,'2026-11-01T14:00:00Z','qa-private-key-norte' FROM users WHERE username='qa-admin';
COMMIT;
""")


def treasury_snapshot(sql):
    return sql("""
SELECT (SELECT md5(jsonb_agg(to_jsonb(p) ORDER BY p.id)::text) FROM gomar.ap_payments p),
       (SELECT count(*) FROM gomar.ap_allocations), (SELECT count(*) FROM gomar.ap_receipts),
       (SELECT count(*) FROM gomar.ap_ledger), (SELECT count(*) FROM gomar.ap_financial_events),
       (SELECT balance_minor FROM gomar.ap_payables WHERE id=16);
""")


def treasury_tools(call, scope, allowed):
    read = "gomar_read_recorded_payment"
    args = {**scope, "payment_id": 55}
    if not allowed:
        call(read, args, denied=True)
        call("gomar_list_recorded_payments", scope, denied=True)
        return
    record = {"id": 55, "status": "draft", "currency": "ARS", "created_date": "2026-11-01", "total_minor": 500}
    assert call(read, args) == record
    assert call("gomar_list_recorded_payments", scope) == {"records": [record]}
    call(read, {**args, "payment_id": 56}, denied=True)
    call(read, {**args, "outlet_id": 2}, denied=True)
    call(read, {**args, "entity_id": 2}, denied=True)


def treasury_checks(page, origin):
    # Fail on a payment/receipt mutation, even in this disposable database.
    writes = []

    def read_only(route):
        if route.request.method not in ("GET", "HEAD"):
            writes.append(route.request.method)
            route.abort()
        else:
            route.continue_()

    pattern = re.compile(rf"^{re.escape(origin)}/api/v1/entities/1/payments(?:[/?]|$)")
    page.route(pattern, read_only)
    try:
        local = page.get_by_label("Local", exact=True)
        local.select_option("1")
        panel = page.locator(".payment-box")
        history = page.locator(".ap-panel .subsection")
        centre = history.locator(".ap-row").filter(has_text="qa-private-payment-centro")
        north = history.locator(".ap-row").filter(has_text="qa-private-payment-norte")
        allocation = panel.get_by_placeholder("Importe a aplicar")
        receipt = panel.locator("input[type=file]")
        post = panel.get_by_role("button", name="Registrar pago", exact=True)

        def show():
            page.get_by_role("button", name="Cuentas a pagar", exact=True).click()

        show()
        centre.get_by_role("button", name="Abrir pago #55 de qa-private-supplier-11", exact=True).click()
        expect(panel).to_contain_text("Borrador #55")
        expect(panel).to_contain_text("Local del pago: QA Centro")
        expect(allocation.locator("..")).to_contain_text("QA Norte")
        allocation.fill("5,00")
        receipt.set_input_files({"name": "centro-qa.png", "mimeType": "image/png", "buffer": b"fixture"})
        expect(panel).to_contain_text("centro-qa.png")
        expect(post).to_be_visible()

        local.select_option("2")
        show()
        expect(panel).to_contain_text("Local del pago: QA Norte")
        expect(panel).not_to_contain_text("Borrador #55")
        expect(panel.get_by_placeholder("0,00")).to_have_value("")
        expect(receipt).to_have_count(0)
        expect(post).to_have_count(0)
        expect(centre).to_contain_text("Seleccioná QA Centro para abrir")
        expect(centre.get_by_role("button", name="Abrir pago #55 de qa-private-supplier-11", exact=True)).to_have_count(0)
        north.get_by_role("button", name="Abrir pago #56 de qa-private-supplier-11", exact=True).click()
        expect(panel).to_contain_text("Borrador #56")
        expect(allocation).to_have_value("")
        expect(receipt).to_have_value("")
        expect(post).to_be_visible()
        assert page.evaluate("document.documentElement.scrollWidth === window.innerWidth")
        assert not writes
    finally:
        page.unroute(pattern, read_only)
