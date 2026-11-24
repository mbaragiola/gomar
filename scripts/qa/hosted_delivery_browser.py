"""Real native delivery inbox and both packaged MCP reads in the scoped fixture."""
from contextlib import ExitStack
import json
import os

from playwright.sync_api import expect, sync_playwright

from hosted_discovery_browser import advance, browser_scope, control, mcp_session, origin

A, B, SELF, EMPTY = (701, 711), (702, 721), (703, 731), (702, 722)
FIRST = {A: "PedidosYa · qa-order-8002", B: "Uber Eats · qa-order-b", SELF: "Rappi · qa-order-self"}
MISSING = {"detail": "No se encontró el local."}
TOOLS = ("gomar_list_recent_delivery", "gomar_delivery_status_summary")
PRIVATE = ("qa-order-", "qa-store", "qa-event", "QA producto", "external_order_id",
           "external_store_id", "pedido_externo", "contact", "items", "scope-branch")


def packaged(rpc, scope, allowed=True):
    entity, outlet = scope
    results = []
    for name in TOOLS:
        result = rpc("tools/call", {"name": name, "arguments": {"entity_id": entity, "outlet_id": outlet}})["result"]
        text = json.dumps(result, ensure_ascii=False)
        assert all(value not in text for value in PRIVATE)
        if not allowed:
            assert result.get("isError") and "structuredContent" not in result
            assert all(field not in text for field in ("orders", "window_limit", "accepted", "received", "cancelled",
                                                        "truncated", "8002", "2030-11-20"))
        else:
            assert not result.get("isError")
            results.append(result["structuredContent"])
    if not allowed:
        return
    recent, status = results
    if scope == A:
        expected_ids = [8002, *range(8101, 8072, -1)]
        assert recent["truncated"] is True
        assert recent["orders"][0]["updated_at"] == "2030-11-20T12:01:00Z"
        assert [row["status"] for row in recent["orders"][:3]] == ["accepted", "received", "cancelled"]
        counts = (1, 98, 1)
    else:
        expected_ids = {B: [8201], SELF: [8301], EMPTY: []}[scope]
        assert recent["truncated"] is False
        counts = {B: (0, 1, 0), SELF: (1, 0, 0), EMPTY: (0, 0, 0)}[scope]
    assert [row["id"] for row in recent["orders"]] == expected_ids
    assert all(set(row) == {"id", "provider", "status", "received_at", "updated_at"} for row in recent["orders"])
    assert status == dict(zip(("window_limit", "received", "accepted", "cancelled"), (100, *counts)))


def path(scope):
    return f"/api/v1/entities/{scope[0]}/outlets/{scope[1]}/delivery/orders"


def open_inbox(page, authority, scope):
    assert page.goto(authority + "/").ok
    page.get_by_role("combobox", name="Entidad", exact=True).select_option(str(scope[0]))
    outlet = page.get_by_role("combobox", name="Local", exact=True)
    expect(outlet.locator(f"option[value='{scope[1]}']")).to_have_count(1)
    outlet.select_option(str(scope[1]))
    with page.expect_response(lambda reply: reply.url == authority + path(scope)
                              and reply.request.method == "GET", timeout=6000) as hit:
        page.get_by_role("button", name="Abrir bandeja de delivery", exact=True).click()
    panel = page.get_by_role("region", name="Bandeja de delivery", exact=True)
    wire = hit.value
    assert wire.status == 200 and wire.headers["cache-control"] == "no-store"
    if scope == EMPTY:
        expect(panel.get_by_text("Todavía no hay pedidos en este local.", exact=True)).to_be_visible()
        assert wire.json() == {"pedidos": []}
    else:
        expect(panel.get_by_role("heading", name=FIRST[scope], exact=True)).to_be_visible()
        assert len(wire.json()["pedidos"]) == (100 if scope == A else 1)
    if scope == A:
        expect(panel.get_by_text("2 × QA producto privado", exact=True).first).to_be_visible()
        expect(panel.get_by_role("heading", name="Rappi · qa-order-8001", exact=True)).to_have_count(0)
        assert wire.json()["pedidos"][-1]["id"] == 8003 and "qa-store" not in wire.text()
    return panel


def reload_denied(page, panel, label):
    with page.expect_response(lambda reply: reply.url == origin + path(A)
                              and reply.request.method == "GET", timeout=5000) as hit:
        panel.get_by_role("button", name=label, exact=True).click()
    wire = hit.value
    assert wire.status == 404 and wire.headers["cache-control"] == "no-store" and wire.json() == MISSING
    expect(panel.get_by_role("alert")).to_have_text("La bandeja de delivery no está habilitada para este local.")
    expect(panel.get_by_role("heading", name=FIRST[A], exact=True)).to_have_count(0)
    expect(panel.locator(".gomar-feature-list")).to_have_count(0)
    expect(panel.get_by_text("QA producto privado", exact=False)).to_have_count(0)


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(executable_path=os.environ.get("QA_CHROME", "/opt/google/chrome/chrome"),
        args=["--no-sandbox", "--disable-background-networking", "--no-proxy-server",
              "--host-resolver-rules=EXCLUDE 127.0.0.1, MAP * ~NOTFOUND"])
    try:
        with browser.new_context(ignore_https_errors=True, service_workers="block") as desktop, browser.new_context(
                ignore_https_errors=True, service_workers="block", is_mobile=True, has_touch=True,
                viewport={"width": 390, "height": 820}) as phone, browser.new_context(
                ignore_https_errors=True, service_workers="block") as ordinary, ExitStack() as stack:
            for context in (desktop, phone, ordinary):
                context.route("**/*", lambda route: route.continue_() if route.request.url.startswith(
                    (origin + "/", control + "/")) else route.abort())
            desk, mobile, unchanged = browser_scope(desktop, origin), browser_scope(phone, origin), browser_scope(ordinary, control)
            sessions = {scope: stack.enter_context(mcp_session(desk, *scope, raw=True)) for scope in (A, B, SELF, EMPTY)}
            for scope, connect in sessions.items():
                packaged(connect, scope)
            packaged(sessions[A], B, False)
            packaged(sessions[A], (701, 712), False)
            panels = {page: open_inbox(page, origin, A) for page in (desk, mobile)}
            for phase, label in (("suspend", "Actualizar pedidos"), ("expire", "Reintentar")):
                advance(phase)
                for page, panel in panels.items():
                    reload_denied(page, panel, label)
                packaged(sessions[A], A, False)
                for scope in (B, SELF, EMPTY):
                    packaged(sessions[scope], scope)
            for scope in (B, SELF, EMPTY):
                open_inbox(desk, origin, scope)
            open_inbox(mobile, origin, B)
            open_inbox(unchanged, control, A)
    finally:
        browser.close()

print("PASS: actual Chrome and both packaged delivery reads preserved scope, privacy and exact inbox windows")
