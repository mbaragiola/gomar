"""Existing native barcode save, packaged proposal and human confirmation in loopback QA."""
from contextlib import ExitStack
import json
import os

from playwright.sync_api import expect, sync_playwright

from hosted_discovery_browser import advance, browser_scope, control, mcp_session, origin
from pos_prebill_checks import use_fixture_connection


A, B, SELF, UNMAPPED = (701, 711, 711), (702, 721, 721), (703, 731, 731), (701, 712, 712)
TOOL = "gomar_propose_pos_barcode"
ACTION = "pos_product_barcode_change"
NONE_FOUND = "No se encontró el local."


def call(rpc, name, args, *, allow=True):
    result = rpc("tools/call", {"name": name, "arguments": args})["result"]
    assert bool(result.get("isError")) != allow
    if not allow:
        assert "structuredContent" not in result
        assert all(value not in json.dumps(result) for value in ("000801", "009801", "QA Producto A", "/pos", "paid-alpha"))
        return None
    return result["structuredContent"]


def fixed_scope(scope):
    entity, outlet, _ = scope
    return {"entity_id": entity, "outlet_id": outlet}


def propose_args(scope, revision, code, slot):
    return {**fixed_scope(scope), "request_id": f"86000000-0000-4000-8000-{slot:012d}",
            "change": {"product_id": scope[2], "expected_revision": revision, "barcode": code}}


def product(rpc, scope):
    return call(rpc, "gomar_read_pos_product", {**fixed_scope(scope), "product_id": scope[2]})


def select_scope(page, scope):
    entity, outlet, _ = scope
    page.get_by_role("combobox", name="Entidad", exact=True).select_option(str(entity))
    local = page.get_by_role("combobox", name="Local", exact=True)
    expect(local.locator(f"option[value='{outlet}']")).to_have_count(1)
    local.select_option(str(outlet))
    expect(local).to_have_value(str(outlet))


def barcode_manager(page, authority, scope):
    use_fixture_connection(page)
    page.goto(authority + "/pos")
    select_scope(page, scope)
    page.get_by_text("Gestionar códigos de barras", exact=True).click()
    manager = page.get_by_role("region", name="Códigos de barras del catálogo", exact=True)
    product_choice = manager.get_by_role("combobox", name="Producto para asignar código", exact=True)
    expect(product_choice).to_be_enabled()
    product_choice.select_option(str(scope[2]))
    return manager


def save(page, manager, authority, scope, notation, allowed=True):
    entity, outlet, ident = scope
    path = f"{authority}/api/v1/entities/{entity}/outlets/{outlet}/pos/catalog/{ident}/barcode"
    manager.get_by_label("Código de barras del producto", exact=True).fill(notation)
    with page.expect_response(lambda x: x.url == path and x.request.method == "PUT", timeout=6000) as response:
        manager.get_by_role("button", name="Guardar código de barras", exact=True).click()
    wire = response.value
    assert wire.status == (200 if allowed else 404) and wire.headers["cache-control"] == "no-store"
    if allowed:
        expect(manager.get_by_role("status")).to_have_text("Código de barras guardado.")
    else:
        assert wire.json() == {"detail": NONE_FOUND}
        expect(manager.get_by_role("alert")).to_have_text(NONE_FOUND)


def prepare_review(page, scope, proposal):
    page.goto(origin)
    select_scope(page, scope)
    page.get_by_role("button", name="Chat y propuestas", exact=True).click()
    page.get_by_role("button", name="Sesiones de propuestas", exact=True).click()
    panel = page.get_by_role("region", name="Sesiones de propuestas", exact=True)
    panel.get_by_role("button", name="Actualizar sesiones y propuestas", exact=True).click()
    panel.get_by_role("button", name=f"Ver propuesta {proposal} de código de barras", exact=True).click()
    preview = panel.get_by_role("region", name="Vista previa de la propuesta", exact=True)
    expect(preview.get_by_text("QA Producto", exact=False)).to_be_visible()
    preview.get_by_role("button", name="Continuar con el código de barras", exact=True).click()
    return panel, preview


def decide(page, panel, preview, scope, proposal, allowed=True):
    entity, outlet, _ = scope
    url = f"{origin}/api/v1/entities/{entity}/outlets/{outlet}/pos-barcode-proposals/{proposal}/decision"
    with page.expect_response(lambda x: x.url == url and x.request.method == "POST", timeout=6000) as response:
        preview.get_by_role("button", name="Confirmar código de barras", exact=True).click()
    wire = response.value
    assert wire.status == (200 if allowed else 404) and wire.headers["cache-control"] == "no-store"
    if allowed:
        expect(panel.get_by_role("status")).to_have_text("Código de barras actualizado.")
    else:
        assert wire.json() == {"detail": NONE_FOUND}


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(executable_path=os.environ.get("QA_CHROME", "/opt/google/chrome/chrome"),
        args=["--no-sandbox", "--disable-background-networking", "--no-proxy-server",
              "--host-resolver-rules=EXCLUDE 127.0.0.1, MAP * ~NOTFOUND"])
    try:
        with browser.new_context(ignore_https_errors=True, service_workers="block") as desk, browser.new_context(
                ignore_https_errors=True, service_workers="block", is_mobile=True, has_touch=True,
                viewport={"width": 390, "height": 820}) as phone, browser.new_context(
                ignore_https_errors=True, service_workers="block") as default, ExitStack() as stack:
            for context in (desk, phone, default):
                context.set_default_timeout(5000)
                context.route("**/*", lambda route: route.continue_() if route.request.url.startswith(
                    (origin + "/", control + "/")) else route.abort())
            owner, mobile, legacy = browser_scope(desk, origin), browser_scope(phone, origin), browser_scope(default, control)
            a_register, c_register = desk.new_page(), desk.new_page()
            creds = {scope: stack.enter_context(mcp_session(mobile if scope == B else owner, *scope[:2],
                    raw=True, proposal_action=ACTION)) for scope in (A, B, SELF)}
            for rpc in creds.values():
                tools = rpc("tools/list", {})["result"]["tools"]
                assert {tool["name"] for tool in tools if not tool.get("annotations", {}).get("readOnlyHint")} == {TOOL}
            a_manager = barcode_manager(a_register, origin, A)
            save(a_register, a_manager, origin, A, "000801")
            own = product(creds[A], A)
            assert own["barcode"] == "000801"
            a_args = propose_args(A, own["revision"], "009801", 101)
            a_pending = call(creds[A], TOOL, a_args)
            assert a_pending["status"] == "pending" and set(a_pending) == {"id", "status", "expires_at"}
            assert call(creds[A], TOOL, a_args) == a_pending
            assert product(creds[A], A) == own
            for lane, rpc in ((B, creds[A]), (UNMAPPED, creds[A])):
                call(rpc, TOOL, propose_args(lane, 2, "009801", 102), allow=False)
            panel, preview = prepare_review(owner, A, a_pending["id"])
            advance("suspend")
            decide(owner, panel, preview, A, a_pending["id"], allowed=False)
            save(a_register, a_manager, origin, A, "009801", allowed=False)
            call(creds[A], TOOL, a_args, allow=False)
            call(creds[A], TOOL, propose_args(A, own["revision"], "009801", 103), allow=False)
            advance("expire")
            save(a_register, a_manager, origin, A, "009802", allowed=False)
            call(creds[A], TOOL, a_args, allow=False)
            for page, scope, code, serial in ((mobile, B, "000802", 104), (c_register, SELF, "000803", 105)):
                rpc = creds[scope]
                before = product(rpc, scope)
                save(page, barcode_manager(page, origin, scope), origin, scope, code)
                after = product(rpc, scope)
                assert after["barcode"] == code and after["revision"] == before["revision"] + 1
                pending = call(rpc, TOOL, propose_args(scope, after["revision"], "", serial))
                assert pending["status"] == "pending" and product(rpc, scope) == after
                panel, preview = prepare_review(page, scope, pending["id"])
                decide(page, panel, preview, scope, pending["id"])
                cleared = product(rpc, scope)
                assert not cleared.get("barcode") and cleared["revision"] == after["revision"] + 1
                assert all(cleared[name] == before[name] for name in ("name", "price_minor", "active"))
            save(legacy, barcode_manager(legacy, control, UNMAPPED), control, UNMAPPED, "001712")
    finally:
        browser.close()
print("PASS: actual Chrome and packaged MCP hosted barcode: expired or suspended A denies; B, self-host and default continue")
