"""Representative synthetic purchase reads and native handoff; no file/payment calls."""
import json

from playwright.sync_api import expect

reference = {"document_type": "invoice", "letter": "A", "point_of_sale": 12, "number": 345}
safe_handoff = "/#alta-cuenta=1:1:11"
handoff_api = "/api/v1/entities/1/outlets/1/documents/11/payable-eligibility-handoff"


def purchase_seed(sql):
    sql("""
BEGIN;
SET search_path=gomar,public;
INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES
 (10,1,'qa-private-supplier-10','00000000010',true),
 (11,1,'qa-private-supplier-11','00000000011',true),
 (12,1,'qa-private-supplier-12','00000000012',true),
 (13,1,'qa-private-supplier-13','00000000013',true);
INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,
  number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at,deleted_at)
VALUES
 (10,1,1,10,'00000000010','invoice','A',12,345,'2026-11-01','ARS',700,'qa-private-description','draft',1,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z',NULL),
 (11,1,1,11,'00000000011','invoice','A',12,345,'2026-11-01','ARS',800,'qa-private-description','reviewed',1,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z',NULL),
 (12,1,2,12,'00000000012','invoice','A',12,345,'2026-11-01','USD',900,'qa-private-foreign','draft',1,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z',NULL),
 (13,1,1,13,'00000000013','invoice','A',12,345,'2026-11-01','ARS',950,'qa-private-deleted','draft',1,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z','2026-11-02T00:00:00Z'),
 (14,1,1,10,'00000000010','invoice','A',12,346,'2026-11-01','ARS',750,'qa-private-description','draft',1,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z',NULL);
COMMIT;
""")


def purchase_snapshot(sql):
    return sql("""
SELECT
 (SELECT md5(jsonb_agg(to_jsonb(d) ORDER BY d.id)::text) FROM gomar.documents d),
 (SELECT count(*) FROM gomar.ap_payables),
 (SELECT count(*) FROM gomar.ap_payments);
""")


def document(doc_id, supplier, total, status):
    return {"id": doc_id, "supplier_id": supplier, "total_minor": total, "status": status,
            "type": "invoice", "currency": "ARS", "issue_date": "2026-11-01"}


def purchase_tools(call, scope, may_handoff):
    args = {**scope, "printed_reference": reference}
    exact = call("gomar_list_documents", args)
    assert exact == {"printed_reference": reference, "documents": [
        document(10, 10, 700, "draft"), document(11, 11, 800, "reviewed"),
    ]}
    drafts = call("gomar_list_documents", {**scope, "status": "draft"})
    assert drafts == {"status_filter": "draft", "documents": [
        document(10, 10, 700, "draft"), document(14, 10, 750, "draft"),
    ]}
    for result in (exact, drafts):
        text = json.dumps(result)
        assert all(omitted not in text for omitted in ("qa-private", "cuit", "attachment"))
    call("gomar_list_documents", {**args, "outlet_id": 2}, denied=True)
    call("gomar_list_documents", {**args, "entity_id": 2}, denied=True)
    call("gomar_list_documents", {**args, "status": "draft"}, denied=True)
    eligibility = "gomar_check_payable_eligibility"
    if not may_handoff:
        call(eligibility, {**scope, "document_id": 11}, denied=True)
        return None
    blocked = call(eligibility, {**scope, "document_id": 10})
    assert blocked.get("eligible") is False and "handoff_url" not in blocked
    accepted = call(eligibility, {**scope, "document_id": 11})
    assert accepted == {"eligible": True, "handoff_url": safe_handoff}
    return accepted["handoff_url"]


def purchase_checks(call, page, reader, origin, scope):
    handoff = purchase_tools(call, scope, may_handoff=True)
    no_role = reader.request.get(origin + handoff_api)
    no_outlet = page.request.get(origin + handoff_api.replace("/outlets/1/", "/outlets/2/"))
    assert no_role.status == no_outlet.status == 404
    for denied in (no_role, no_outlet):
        assert "qa-private" not in denied.text() and "printed_reference" not in denied.text()

    # Navigate only to the returned relative handoff. Never request any file
    # content or payment mutation, even in the synthetic browser.
    forbidden = []
    def protect(route):
        url = route.request.url
        if not url.startswith(origin + "/"):
            route.abort()
            return
        if "/api/" in url and ("/attachment" in url or "/payment" in url
                               or route.request.method not in ("GET", "HEAD")):
            forbidden.append(route.request.method)
            route.abort()
            return
        route.continue_()
    page.route("**/*", protect)

    page.goto(origin + handoff)
    expect(page.get_by_role("heading", name="Revisar comprobante para cuentas a pagar")).to_be_visible()
    expect(page.get_by_text("Factura A 00012-00000345", exact=True)).to_be_visible()
    expect(page.get_by_text("Comprobante 11 · Local 1", exact=True)).to_be_visible()
    assert "qa-private" not in page.locator("body").inner_text()
    page.get_by_role("link", name="Ir a Gomar").click()
    page.get_by_label("Entidad", exact=True).select_option("1")
    page.get_by_label("Local", exact=True).select_option("1")
    finder = page.locator("details.document-reference-finder")
    finder.get_by_text("Buscar por referencia impresa", exact=True).click()
    finder.get_by_label("Tipo de comprobante").select_option("invoice")
    finder.get_by_label("Letra impresa").select_option("A")
    finder.get_by_label("Punto de venta impreso").fill("00012")
    finder.get_by_label("Número impreso").fill("00000345")
    page.get_by_role("button", name="Buscar en este local").click()
    expect(page.get_by_text("Hay varias coincidencias. Abrí la que corresponda.", exact=False)).to_be_visible()
    for row in (10, 11):
        expect(page.get_by_role("button", name=f"Comprobante {row}", exact=False)).to_be_visible()
    for row in (12, 13, 14):
        expect(page.get_by_role("button", name=f"Comprobante {row}", exact=False)).to_have_count(0)

    page.get_by_role("button", name="Quitar búsqueda").click()
    page.get_by_role("button", name="Ver borradores").click()
    for row in (10, 14):
        expect(page.get_by_role("button", name=f"Comprobante {row}", exact=False)).to_be_visible()
    for row in (11, 12, 13):
        expect(page.get_by_role("button", name=f"Comprobante {row}", exact=False)).to_have_count(0)
    page.unroute("**/*", protect)
    assert not forbidden, "purchase journey attempted a file or payment request"
