"""Actual read-only Caja handoff and packaged MCP for isolated paid timing."""
from contextlib import ExitStack
import json
import os

from playwright.sync_api import expect, sync_playwright

from hosted_discovery_browser import advance, browser_scope, control, mcp_session, origin
from pos_prebill_checks import use_fixture_connection


TOOL = "gomar_read_pos_sale_time"
HEADING = "Horarios de la venta de Caja"
FAILED = "No se pudo consultar la referencia con esta cuenta y local."
REGISTERED = "2026-12-01T03:07:00Z"
UNVERIFIED = "2026-12-01T02:58:00Z"
A, UNMAPPED, B, SELF = [(entity, outlet, f"92000000-0000-4000-8000-{suffix:012d}")
                         for entity, outlet, suffix in ((701, 711, 71), (701, 712, 72), (702, 721, 73), (703, 731, 74))]


def handoff(scope):
    entity, outlet, ref = scope
    return f"/pos#entity={entity}&outlet={outlet}&venta={ref}"


def packaged(rpc, scope, allowed):
    entity, outlet, ref = scope
    reply = rpc("tools/call", {"name": TOOL, "arguments": {"entity_id": entity, "outlet_id": outlet, "request_id": ref}})
    result = reply["result"]
    if not allowed:
        assert result.get("isError") and "structuredContent" not in result
        wire = json.dumps(result)
        assert all(field not in wire for field in (REGISTERED, UNVERIFIED, "handoff_url", "till_time_status", "/pos#", ref))
        return None
    assert not result.get("isError")
    evidence = result["structuredContent"]
    assert evidence["request_id"] == ref and evidence["handoff_url"] == handoff(scope)
    assert evidence["registered_at_utc"] == REGISTERED
    if scope == B:
        assert evidence["till_time_status"] == "not_reported" and "till_reported_at_utc" not in evidence
    else:
        assert evidence["till_time_status"] == "unverified" and evidence["till_reported_at_utc"] == UNVERIFIED
    return evidence["handoff_url"]


def check(page, authority, scope, *, allow, link=None, open_link=True):
    entity, outlet, ref = scope
    target = f"{authority}/api/v1/entities/{entity}/outlets/{outlet}/pos/sale-times/{ref}"
    seen = []

    def remember(req):
        if req.url.startswith(authority + "/api/"):
            seen.append((req.method, req.url))

    page.on("request", remember)
    try:
        if open_link:
            page.goto(authority + (link or handoff(scope)))
        expect(page.get_by_role("heading", name=HEADING, exact=True)).to_be_visible()
        if open_link:
            assert page.locator("main time").count() == 0
            assert not seen  # Do not mount/start Caja or automatically query timing.
        consult = page.get_by_role("button", name="Consultar horarios", exact=True)
        expect(consult).to_be_enabled(timeout=4000)
        with page.expect_response(lambda x: x.url == target, timeout=5000) as response:
            consult.click()
        wire = response.value
        assert wire.status == (200 if allow else 404) and wire.headers["cache-control"] == "no-store"
        if allow:
            expect(page.locator(f"main time[datetime='{REGISTERED}']")).to_be_visible()
            if scope == B:
                expect(page.get_by_text("Sin hora informada por la caja en el registro de Gomar.")).to_be_visible()
            else:
                expect(page.locator(f"main time[datetime='{UNVERIFIED}']")).to_be_visible()
        else:
            expect(page.get_by_role("alert")).to_have_text(FAILED)
            expect(page.locator("main time")).to_have_count(0)
            raw = wire.text()
            assert all(field not in raw for field in ("registered_at", "till_", "handoff", ref, REGISTERED, UNVERIFIED))
        assert seen == [("GET", target)]
    finally:
        page.remove_listener("request", remember)


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(executable_path=os.environ.get("QA_CHROME", "/opt/google/chrome/chrome"),
        args=["--no-sandbox", "--disable-background-networking", "--no-proxy-server",
              "--host-resolver-rules=EXCLUDE 127.0.0.1, MAP * ~NOTFOUND"])
    try:
        with browser.new_context(ignore_https_errors=True, service_workers="block") as hosted, browser.new_context(
                ignore_https_errors=True, service_workers="block", is_mobile=True, has_touch=True,
                viewport={"width": 390, "height": 820}) as phone, browser.new_context(
                ignore_https_errors=True, service_workers="block") as ordinary, ExitStack() as stack:
            for context in (hosted, phone, ordinary):
                context.route("**/*", lambda route: route.continue_() if route.request.url.startswith(
                    (origin + "/", control + "/")) else route.abort())
            desk, mobile, default = browser_scope(hosted, origin), browser_scope(phone, origin), browser_scope(ordinary, control)
            # The loopback-only namespace reports offline to Chrome. Only
            # correct the OS hint; every lookup uses the genuine local API.
            for page in (desk, mobile, default):
                use_fixture_connection(page)
            creds = {scope: stack.enter_context(mcp_session(desk, *scope[:2], raw=True)) for scope in (A, B, SELF, UNMAPPED)}
            a_link = packaged(creds[A], A, True)
            for scope in (B, SELF):
                packaged(creds[scope], scope, True)
            packaged(creds[A], B, False)
            packaged(creds[A], UNMAPPED, False)
            packaged(creds[UNMAPPED], UNMAPPED, False)
            check(mobile, origin, UNMAPPED, allow=False)
            for page in (desk, mobile):
                check(page, origin, A, allow=True, link=a_link)
            for phase in ("suspend", "expire"):
                advance(phase)
                for page in (desk, mobile):
                    check(page, origin, A, allow=False, link=a_link, open_link=(phase != "suspend"))
                packaged(creds[A], A, False)
                for scope in (B, SELF):
                    link = packaged(creds[scope], scope, True)
                    check(desk, origin, scope, allow=True, link=link)
                check(default, control, A, allow=True, link=a_link)
                check(default, control, UNMAPPED, allow=True)
    finally:
        browser.close()
print("PASS: actual Chrome and packaged MCP hosted POS sale timing: exact A replay denied; B, self-host and default unchanged")
