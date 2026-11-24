"""Actual local stock panel, native movement GET, and five packaged MCP reads."""
from contextlib import ExitStack
import json
import os

from playwright.sync_api import expect, sync_playwright

from hosted_discovery_browser import advance, browser_scope, control, mcp_session, origin, request

A = (701, 711, 9103)
B = (702, 721, 9201)
SELF = (703, 731, 9301)
EMPTY = (702, 722, 0)
DISABLED = (702, 723, 9601)
LABELS = {A: "QA A Sano", B: "QA B Visible", SELF: "QA Propio Visible"}
MISSING = {"detail": "No se encontró el local."}
ZONE = "America/Argentina/Buenos_Aires"
DATE = {"start_date": "2026-11-18", "days": 1}
TOOLS = (
    "gomar_list_stock",
    "gomar_read_stock_product",
    "gomar_stock_alert_summary",
    "gomar_stock_movements",
    "gomar_list_stock_movements",
)


def stock_url(scope):
    entity, outlet, _ = scope
    return f"/api/v1/entities/{entity}/outlets/{outlet}/pos/stock"


def tool(rpc, args, idx, *, allowed=True, **extra):
    result = rpc("tools/call", {"name": TOOLS[idx], "arguments": args | extra})["result"]
    if not allowed:
        assert result.get("isError") and "structuredContent" not in result
        blob = json.dumps(result, ensure_ascii=False)
        assert all(value not in blob for value in (
            "QA A", "QA producto", "stock_movements", "balance", "tracked", "minimum", "negative",
            "recorded_at", "next_product_id", "movement_count", "branch_ref", "paid-alpha",
        ))
        return None
    assert not result.get("isError")
    return result["structuredContent"]


def packaged(rpc, scope, allowed=True):
    entity, outlet, product = scope
    args = {"entity_id": entity, "outlet_id": outlet}
    stock = tool(rpc, args, 0, allowed=allowed)
    detail = tool(rpc, args, 1, allowed=allowed, product_id=product)
    alerts = tool(rpc, args, 2, allowed=allowed)
    summary = tool(rpc, args, 3, allowed=allowed, product_id=product, **DATE)
    moves = tool(rpc, args, 4, allowed=allowed, product_id=product, **DATE)
    if not allowed:
        tool(rpc, args, 0, allowed=False, alert="any", after_product_id=9101)
        tool(rpc, args, 4, allowed=False, product_id=product, after_id=1, **DATE)
        return
    assert stock["enabled"] and any(row["id"] == product and row["name"] == LABELS[scope] for row in stock["products"])
    assert detail["id"] == product and detail["outlet_enabled"] and detail["product_enabled"]
    assert summary["product_id"] == product
    for value in (summary, moves):
        assert value["start_date"] == DATE["start_date"]
        assert value["end_date_exclusive"] == "2026-11-19" and value["timezone"] == ZONE
    kinds = {part["kind"]: (part["movement_count"], part["net_units"]) for part in summary["kinds"]}
    if scope != A:
        assert alerts == {"enabled": True, "tracked": 1, "negative": 0, "at_or_below_minimum": 1, "healthy": 0}
        assert detail["balance"] == 0 and detail["alert"] == "low" and moves["movements"] == []
        assert all(value == (0, 0) for value in kinds.values())
        return
    assert detail["balance"] == 69 and "alert" not in detail
    assert alerts == {"enabled": True, "tracked": 32, "negative": 1, "at_or_below_minimum": 30, "healthy": 1}
    assert kinds == {"recepcion": (31, 62), "venta": (1, -1), "merma": (0, 0), "conteo": (0, 0)}
    assert moves["movements"] == [{"kind": "recepcion", "delta": 2, "recorded_at": "2026-11-18T00:00:00-03:00"}] * 30
    second = tool(rpc, args, 4, product_id=product, after_id=moves["next_id"], **DATE)
    assert second["movements"] == [
        {"kind": "recepcion", "delta": 2, "recorded_at": "2026-11-18T00:00:00-03:00"},
        {"kind": "venta", "delta": -1, "recorded_at": "2026-11-18T09:00:00-03:00"},
    ] and "next_id" not in second
    any_alert = tool(rpc, args, 0, alert="any")
    tail = tool(rpc, args, 0, alert="any", after_product_id=any_alert["next_product_id"])
    assert any_alert["alert_filter"] == "any" and len(any_alert["products"]) == 30
    assert {row["alert"] for row in any_alert["products"]} == {"negative", "low"}
    assert [p["id"] for p in tail["products"]] == [9134] and "next_product_id" not in tail
    assert [p["id"] for p in tool(rpc, args, 0, alert="negative")["products"]] == [9101]
    assert all(p["alert"] == "low" for p in tool(rpc, args, 0, alert="low")["products"])
    assert stock["next_product_id"] > 9105 and len(stock["products"]) == 30
    by_id = {p["id"]: p for p in stock["products"]}
    assert by_id[9104]["enabled"] is False and "alert" not in by_id[9104] and 9105 not in by_id


def open_stock(page, authority, scope):
    entity, outlet, _ = scope
    assert page.goto(authority + "/").ok
    page.get_by_role("combobox", name="Entidad", exact=True).select_option(str(entity))
    local = page.get_by_role("combobox", name="Local", exact=True)
    expect(local.locator(f"option[value='{outlet}']")).to_have_count(1)
    local.select_option(str(outlet))
    with page.expect_response(lambda r: r.url == authority + stock_url(scope) and r.request.method == "GET",
                              timeout=6000) as hit:
        page.get_by_role("button", name="Ver stock del local", exact=True).click()
    assert hit.value.status == 200 and hit.value.headers["cache-control"] == "no-store"
    panel = page.get_by_role("region", name="Stock del local", exact=True)
    if scope == EMPTY:
        expect(panel.get_by_text("Todavía no hay productos con seguimiento de stock.", exact=True)).to_be_visible()
    elif scope == DISABLED:
        expect(panel.get_by_text("El seguimiento de stock está desactivado para este local.", exact=True)).to_be_visible()
    else:
        expect(panel.get_by_role("heading", name=LABELS[scope], exact=True)).to_be_visible()
    if scope == A:
        expect(panel.get_by_text("Stock negativo. Revisá el saldo.", exact=True)).to_be_visible()
        expect(panel.get_by_text("Stock por debajo del mínimo.", exact=True).first).to_be_visible()
        expect(panel.get_by_text("Inactivo", exact=True)).to_be_visible()
        expect(panel.get_by_role("heading", name="QA A Solo catálogo", exact=True)).to_have_count(0)
    return panel


def native_movement(page, scope, allowed):
    if not scope[2]:
        return
    status, data = request(page, stock_url(scope) + f"/products/{scope[2]}/movements")
    if not allowed:
        assert (status, data) == (404, MISSING)
    else:
        assert status == 200 and isinstance(data["movimientos"], list) and not data["truncado"]
        if scope == A:
            assert sum(r["variacion"] for r in data["movimientos"]) == 69
            assert sum(r["tipo"] == "venta" for r in data["movimientos"]) == 1


def refresh_denied(page, panel):
    button = panel.get_by_role("button", name="Actualizar stock", exact=True)
    with page.expect_response(lambda r: r.url == origin + stock_url(A) and r.request.method == "GET",
                              timeout=5000) as hit:
        button.click()
    assert hit.value.status == 404 and hit.value.json() == MISSING
    assert hit.value.headers["cache-control"] == "no-store"
    expect(panel.get_by_role("alert")).to_have_text("El stock no está habilitado para este local.")
    expect(panel.get_by_role("heading", name=LABELS[A], exact=True)).to_have_count(0)
    expect(panel.get_by_text("Saldo estimado:", exact=False)).to_have_count(0)


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
            sessions = {scope: stack.enter_context(mcp_session(desk, *scope[:2], raw=True)) for scope in (A, B, SELF, EMPTY)}
            for scope in (A, B, SELF):
                packaged(sessions[scope], scope)
            vacant = {"entity_id": EMPTY[0], "outlet_id": EMPTY[1]}
            assert tool(sessions[EMPTY], vacant, 0, alert="low") == {"enabled": True, "products": [], "alert_filter": "low"}
            assert tool(sessions[EMPTY], vacant, 2) == {
                "enabled": True, "tracked": 0, "healthy": 0, "negative": 0, "at_or_below_minimum": 0,
            }
            packaged(sessions[A], B, False)
            packaged(sessions[A], (701, 712, 9401), False)
            panels = {page: open_stock(page, origin, A) for page in (desk, mobile)}
            for page in panels:
                native_movement(page, A, True)
            advance("suspend")
            for page, panel in panels.items():
                refresh_denied(page, panel)
                native_movement(page, A, False)
            packaged(sessions[A], A, False)
            advance("expire")
            for page in panels:
                assert request(page, stock_url(A)) == (404, MISSING)
                native_movement(page, A, False)
            packaged(sessions[A], A, False)
            for scope in (B, SELF, EMPTY, DISABLED):
                open_stock(desk, origin, scope)
                native_movement(desk, scope, True)
                if scope in (B, SELF):
                    packaged(sessions[scope], scope)
            open_stock(unchanged, control, A)
            native_movement(unchanged, A, True)
    finally:
        browser.close()

print("PASS: actual Chrome and all five packaged hosted stock reads protected exact outlets, kept B/self-host/default, and changed no inventory")
