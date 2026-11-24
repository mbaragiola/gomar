"""Real staff reservation queue in Chrome and the three packaged MCP reads."""
from contextlib import ExitStack
import json
import os

from playwright.sync_api import expect, sync_playwright

from hosted_discovery_browser import advance, browser_scope, control, mcp_session, origin

A, B, SELF, EMPTY = (701, 711, 2), (702, 721, 5), (703, 731, 6), (702, 722, 5)
LABELS = {A: "QA Invitada A", B: "QA Invitada B", SELF: "QA Invitada propia"}
PRIVATE = ("guest_name", "table_label", "phone_e164", "customer_id", "note", "cancellation_policy",
           "QA ", "12025550123", "scope-branch", "paid-alpha", "5bf11003")
DATE = "2030-11-20"
ZONE = "America/Argentina/Buenos_Aires"
MISSING = "No se encontró el local."
TOOLS = ("gomar_list_reservation_requests", "gomar_read_reservation_request", "gomar_reservation_status")


def ref(number):
    return f"{number:032x}"


def queue_url(scope):
    return f"/api/v1/entities/{scope[0]}/outlets/{scope[1]}/reservations?date={DATE}"


def tool(rpc, scope, idx, allowed=True, **extra):
    entity, outlet, number = scope
    args = {"entity_id": entity, "outlet_id": outlet, "date": DATE}
    if idx == 1:
        args["reservation_id"] = ref(number)
    result = rpc("tools/call", {"name": TOOLS[idx], "arguments": args | extra})["result"]
    wire = json.dumps(result)
    assert all(private not in wire for private in PRIVATE)
    if not allowed:
        assert result.get("isError") and "structuredContent" not in result
        assert all(field not in wire for field in ("requests", "start_at", "party_size", "request_count",
                                                   "counts", "next_id", ref(number)))
        return None
    assert not result.get("isError")
    return result["structuredContent"]


def packaged(rpc, scope, allowed=True):
    listed = tool(rpc, scope, 0, allowed)
    detail = tool(rpc, scope, 1, allowed and scope != EMPTY)
    counts = tool(rpc, scope, 2, allowed)
    if not allowed:
        tool(rpc, scope, 0, False, after_id=ref(127))
        return
    assert listed["date"] == counts["date"] == DATE and listed["timezone"] == counts["timezone"] == ZONE
    totals = {row["status"]: (row["request_count"], row["party_size_total"]) for row in counts["counts"]}
    if scope == EMPTY:
        assert listed["requests"] == [] and "next_id" not in listed
        assert totals == {"pending": (0, 0), "confirmed": (0, 0), "cancelled": (0, 0)}
        return
    assert detail == listed["requests"][0] and detail["id"] == ref(scope[2])
    assert set(detail) == {"id", "status", "party_size", "start_at"}
    if scope == A:
        assert detail == {"id": ref(2), "status": "pending", "party_size": 2, "start_at": DATE + "T00:00:00-03:00"}
        assert totals == {"pending": (1, 2), "confirmed": (1, 3), "cancelled": (30, 30)}
        assert len(listed["requests"]) == 30 and listed["next_id"] == ref(127)
        tail = tool(rpc, scope, 0, after_id=listed["next_id"])
        assert [row["id"] for row in tail["requests"]] == [ref(128), ref(129)] and "next_id" not in tail
        tool(rpc, scope, 0, False, after_id=ref(5))  # same date, different outlet
        tool(rpc, scope, 1, False, reservation_id=ref(4))  # exact next local midnight
    elif scope == B:
        assert totals == {"pending": (1, 4), "confirmed": (0, 0), "cancelled": (0, 0)}
    else:
        assert totals == {"pending": (0, 0), "confirmed": (0, 0), "cancelled": (1, 5)}


def open_reservations(page, authority, scope):
    assert page.goto(authority + "/").ok
    page.get_by_role("combobox", name="Entidad", exact=True).select_option(str(scope[0]))
    local = page.get_by_role("combobox", name="Local", exact=True)
    expect(local.locator(f"option[value='{scope[1]}']")).to_have_count(1)
    local.select_option(str(scope[1]))
    page.get_by_role("button", name="Ver reservas del local", exact=True).click()
    panel = page.get_by_role("region", name="Reservas del local", exact=True)
    field = panel.get_by_label("Fecha", exact=True)
    expect(field).to_be_enabled()
    field.fill(DATE)
    reload_queue(page, panel, authority, scope, True)
    return panel


def reload_queue(page, panel, authority, scope, allowed):
    trigger = panel.get_by_role("button", name="Ver reservas", exact=True)
    with page.expect_response(lambda reply: reply.url == authority + queue_url(scope)
                              and reply.request.method == "GET", timeout=6000) as hit:
        trigger.click()
    wire = hit.value
    assert wire.headers["cache-control"] == "no-store"
    if not allowed:
        assert wire.status == 404 and wire.json() == {"detail": MISSING}
        expect(panel.get_by_role("alert")).to_have_text(MISSING)
        expect(panel.get_by_text(LABELS[A], exact=True)).to_have_count(0)
        expect(panel.get_by_text("QA Invitado confirmado", exact=True)).to_have_count(0)
        expect(panel.get_by_text("QA lista paginada", exact=True)).to_have_count(0)
        expect(panel.locator(".gomar-feature-list")).to_have_count(0)
        return
    assert wire.status == 200
    assert all(value not in wire.text() for value in ("12025550123", "5bf11003", "QA no proyectar", "customer_id", "scope-branch"))
    if scope == EMPTY:
        assert wire.json() == {"requests": [], "truncated": False}
        expect(panel.get_by_text("No hay reservas para esa fecha.", exact=True)).to_be_visible()
    else:
        expect(panel.get_by_text(LABELS[scope], exact=True)).to_be_visible()
        assert len(wire.json()["requests"]) == (32 if scope == A else 1)
    if scope == A:
        expect(panel.get_by_text("QA Invitado confirmado", exact=True)).to_be_visible()
        expect(panel.get_by_text("QA día anterior", exact=True)).to_have_count(0)
        expect(panel.get_by_text("QA día siguiente", exact=True)).to_have_count(0)


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(executable_path=os.environ.get("QA_CHROME", "/opt/google/chrome/chrome"),
        args=["--no-sandbox", "--disable-background-networking", "--no-proxy-server",
              "--host-resolver-rules=EXCLUDE 127.0.0.1, MAP * ~NOTFOUND"])
    try:
        with browser.new_context(ignore_https_errors=True, service_workers="block") as hosted, browser.new_context(
                ignore_https_errors=True, service_workers="block", is_mobile=True, has_touch=True,
                viewport={"width": 390, "height": 820}) as phone, browser.new_context(
                ignore_https_errors=True, service_workers="block") as standard, ExitStack() as stack:
            for context in (hosted, phone, standard):
                context.route("**/*", lambda route: route.continue_() if route.request.url.startswith(
                    (origin + "/", control + "/")) else route.abort())
            desk, mobile, normal = browser_scope(hosted, origin), browser_scope(phone, origin), browser_scope(standard, control)
            sessions = {scope: stack.enter_context(mcp_session(desk, *scope[:2], raw=True))
                        for scope in (A, B, SELF, EMPTY)}
            for scope, client in sessions.items():
                packaged(client, scope)
            packaged(sessions[A], B, False)
            packaged(sessions[A], (701, 712, 7), False)
            panels = {page: open_reservations(page, origin, A) for page in (desk, mobile)}
            for phase in ("suspend", "expire"):
                advance(phase)
                for page, panel in panels.items():
                    reload_queue(page, panel, origin, A, False)
                packaged(sessions[A], A, False)
                for scope in (B, SELF, EMPTY):
                    packaged(sessions[scope], scope)
            for scope in (B, SELF, EMPTY):
                open_reservations(desk, origin, scope)
            open_reservations(mobile, origin, B)
            open_reservations(normal, control, A)
    finally:
        browser.close()

print("PASS: actual native Chrome and all three packaged reservation queue reads scoped the same queue with no data or access writes")
