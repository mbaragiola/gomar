"""Exact synthetic Gomar HTTPS routes in desktop/mobile Chrome and packaged MCP."""
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import time

from playwright.sync_api import expect, sync_playwright


origin, mcp, certificate, scratch = sys.argv[1:]
chrome = os.environ.get("QA_CHROME", "/opt/google/chrome/chrome")


def sign_in(page, actor):
    response = page.request.get(f"{origin}/__signup-fixture-sign-in/{actor}")
    assert response.status == 204


def post(page, endpoint, payload):
    return page.evaluate("""async ([path, body]) => {
      const csrf = document.cookie.match(/(?:^|; )csrftoken=([^;]+)/)?.[1];
      const r = await fetch(path, {method: 'POST', cache: 'no-store',
        headers:{'Content-Type':'application/json','X-CSRFToken':csrf},
        body: JSON.stringify(body)});
      return [r.status, await r.text()];
    }""", [endpoint, payload])


def open_context(browser, mobile=False):
    context = browser.new_context(ignore_https_errors=True, is_mobile=mobile, has_touch=mobile,
                                  viewport={"width": 390 if mobile else 1280, "height": 820},
                                  service_workers="block")
    context.route("**/*", lambda route: route.continue_() if route.request.url.startswith(origin + "/") else route.abort())
    return context


def expect_visible_spacing(locator, exact):
    assert locator.text_content() == exact
    # Accessible names and Playwright's default text assertions normalize
    # spaces. Measure rendered glyph ranges so a collapsing CSS rule fails.
    assert locator.evaluate("""node => {
      const label = node.firstChild;
      const offset = label.data.indexOf('  ');
      if (offset < 0 || getComputedStyle(node).whiteSpace !== 'break-spaces') return false;
      const spaces = new Range();
      spaces.setStart(label, offset);
      spaces.setEnd(label, offset + 1);
      const one = spaces.getBoundingClientRect().width;
      spaces.setEnd(label, offset + 2);
      return one > 0 && spaces.getBoundingClientRect().width > one + 1;
    }""")


def reopened_status(page, actor, state, title):
    with page.expect_response(lambda r: r.url.endswith("/alta/estado")) as waiting:
        assert page.reload().status == 200
    result = waiting.value
    assert result.status == 200 and result.headers["cache-control"] == "private, no-store"
    assert result.json() == {"state": state, "organization": f"QA Organización  {actor}",
                             "branches": ["Local  Centro", "Norte"]}
    expect(page.get_by_role("heading", name=title)).to_be_visible()
    expect(page.get_by_text("no acredita un pago ni activa el acceso", exact=False)).to_be_visible()
    expect(page.get_by_role("button", name="Confirmar solicitud")).to_have_count(0)
    expect(page.get_by_role("button", name="Ver solicitud")).to_have_count(0)
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")


def prepare_after_confirmation(actor):
    # Empty signals in the test-owned directory only. There is no
    # preparation route, no owner credential, and no approval in the signal.
    ready = Path(scratch) / f"ready-{actor}"
    with os.fdopen(os.open(ready, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "wb"):
        pass
    staged = Path(scratch) / f"staged-{actor}"
    deadline = time.monotonic() + 12
    while not staged.is_file() and time.monotonic() < deadline:
        time.sleep(.02)
    assert staged.is_file(), "protected fixture did not prepare and restart"



def status_visibility_page(context, visible):
    page = context.new_page()
    page.add_init_script("""(() => {
      window.signupFixtureVisible = __VISIBLE__;
      window.signupFixtureSawOldStatus = false;
      Object.defineProperty(document, 'visibilityState', {
        configurable:true, get: () => window.signupFixtureVisible ? 'visible' : 'hidden',
      });
      new MutationObserver(() => {
        if (document.body?.textContent.includes('QA Organización')) window.signupFixtureSawOldStatus = true;
      }).observe(document, {childList:true, subtree:true, characterData:true});
    })()""".replace("__VISIBLE__", "true" if visible else "false"))
    return page


def discard_status_while_hidden(context, actor):
    # Supply the exact live HTTPS response, but hide the page before its
    # delivery without sending an event. The final completion guard must
    # drop it and do an independent status check on foreground.
    page = status_visibility_page(context, True)
    requests = []
    page.on("request", lambda req: requests.append(req) if req.url.endswith("/alta/estado") else None)

    def deliver_while_hidden(route):
        upstream = route.fetch()
        assert upstream.status == 200 and upstream.json()["organization"] == f"QA Organización  {actor}"
        page.evaluate("window.signupFixtureVisible = false")
        route.fulfill(response=upstream)

    try:
        page.route(origin + "/api/v1/alta/estado", deliver_while_hidden, times=1)
        with page.expect_response(lambda r: r.url.endswith("/alta/estado")) as pending:
            assert page.goto(origin + "/alta").status == 200
        pending.value.finished()
        # Give the page's fetch/JSON completion a turn without changing visibility.
        page.evaluate("() => new Promise(resolve => setTimeout(resolve, 25))")
        assert page.evaluate("document.visibilityState") == "hidden" and len(requests) == 1
        assert not page.evaluate("window.signupFixtureSawOldStatus")
        with page.expect_response(lambda r: r.url.endswith("/alta/estado")) as fresh:
            page.evaluate("""() => {
              window.signupFixtureVisible = true;
              document.dispatchEvent(new Event('visibilitychange'));
            }""")
        assert fresh.value.status == 200 and fresh.value.json()["state"] == "prepared"
        expect(page.get_by_role("heading", name="Preparación registrada")).to_be_visible()
        assert len(requests) == 2
    finally:
        page.close()


def hidden_status_after_rotation(context, actor):
    # Controlled visibility on the real page, with actual Gomar session
    # rotation in the same context and a real HTTPS status request.
    page = status_visibility_page(context, False)
    requests = []
    page.on("request", lambda req: requests.append(req) if req.url.endswith("/alta/estado") else None)
    try:
        assert page.goto(origin + "/alta").status == 200
        assert page.evaluate("document.visibilityState") == "hidden"
        expect(page.get_by_role("heading", name="Consultando el estado")).to_be_visible()
        assert not requests and not page.evaluate("window.signupFixtureSawOldStatus")
        sign_in(page, actor)  # Rotates the exact original session in this context.
        assert not requests and not page.evaluate("window.signupFixtureSawOldStatus")
        with page.expect_response(lambda r: r.url.endswith("/alta/estado")) as waiting:
            page.evaluate("""() => {
              window.signupFixtureVisible = true;
              document.dispatchEvent(new Event('visibilitychange'));
            }""")
        denied = waiting.value
        assert denied.status == 404 and "QA Organización" not in denied.text()
        expect(page.get_by_role("heading", name="Solicitud no disponible")).to_be_visible()
        expect(page.get_by_role("heading", name="Preparación registrada")).to_have_count(0)
        assert len(requests) == 1 and not page.evaluate("window.signupFixtureSawOldStatus")
    finally:
        page.close()


def check_native(browser, actor, mobile):
    with open_context(browser, mobile) as context:
        page = context.new_page()
        assert page.goto(origin + "/alta").status == 404
        sign_in(page, actor)
        assert page.goto(origin + "/__signup-fixture-disabled").status == 404
        assert page.goto(origin + "/alta?owner=staff").status == 404
        assert page.goto(origin + "/alta#unsafe").status == 200
        expect(page.get_by_role("heading", name="Solicitud no disponible")).to_be_visible()
        expect(page.get_by_role("button", name="Ver solicitud")).to_have_count(0)
        assert page.goto(origin + "/alta").status == 200
        expect(page.get_by_role("heading", name="Revisá el alta")).to_be_visible()
        writes = []
        page.on("request", lambda req: writes.append(req) if req.url.endswith("/alta/confirmar") else None)
        button = page.get_by_role("button", name="Ver solicitud")
        button.focus()
        button.press("Enter")
        heading = page.get_by_role("heading", name=f"QA Organización {actor}")
        branches = page.get_by_role("list", name="Locales incluidos").locator("li")
        expect(heading).to_be_focused()
        expect(branches).to_have_text(["Local Centro", "Norte"])
        expect_visible_spacing(heading, f"QA Organización  {actor}")
        expect_visible_spacing(branches.first, "Local  Centro")
        expect(page.get_by_text("Esto no confirma un pago ni habilita el acceso.", exact=False)).to_be_visible()
        assert not writes
        assert page.locator("main").evaluate("node => node.scrollWidth <= node.clientWidth")
        if mobile and os.environ.get("GOMAR_QA_SIGNUP_SCREENSHOT"):
            output = Path(os.environ["GOMAR_QA_SIGNUP_SCREENSHOT"])
            assert output.is_absolute() and output.name == "signup-synthetic-mobile.png"
            parent = output.parent.lstat()
            assert stat.S_ISDIR(parent.st_mode) and stat.S_IMODE(parent.st_mode) == 0o700 and parent.st_uid == os.geteuid()
            with os.fdopen(os.open(output, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "wb") as image:
                image.write(page.screenshot())
        if mobile:
            page.set_viewport_size({"width": 320, "height": 820})
            expect(heading).to_be_visible()
            expect(branches.first).to_be_visible()
            assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")
        page.get_by_role("button", name="Volver sin confirmar").click()
        expect(page.get_by_role("heading", name="Confirmá tu solicitud")).to_be_focused()
        assert not writes
        with page.expect_response(lambda r: r.url.endswith("/alta/revisar")) as waiting:
            page.get_by_role("button", name="Ver solicitud").click()
        body = waiting.value.json()
        assert set(body) == {"organization", "branches", "challenge"}
        with open_context(browser, mobile) as moved:
            assert moved.request.get(f"{origin}/__signup-fixture-sign-in/{actor}").status == 204
            csrf = next(cookie["value"] for cookie in moved.cookies() if cookie["name"] == "csrftoken")
            denied = moved.request.post(origin + "/api/v1/alta/confirmar",
                                       headers={"Origin": origin, "X-CSRFToken": csrf},
                                       data={"challenge": body["challenge"], "confirm": True})
            assert denied.status == 404 and "private-" not in denied.text() and body["organization"] not in denied.text()
        submit = page.get_by_role("button", name="Confirmar solicitud")
        submit.focus()
        with page.expect_response(lambda r: r.url.endswith("/alta/confirmar")) as waiting:
            submit.press("Enter")
        assert waiting.value.status == 204
        expect(page.get_by_role("heading", name="Confirmación recibida")).to_be_focused()
        expect(page.get_by_text("La puesta en marcha sigue pendiente.", exact=False)).to_be_visible()
        assert len(writes) == 1 and writes[0].post_data_json == {"challenge": body["challenge"], "confirm": True}
        assert post(page, "/api/v1/alta/confirmar", {"challenge": body["challenge"], "confirm": True})[0] == 404
        assert post(page, "/api/v1/auth/mcp-token", {"entity_id": 711, "outlet_id": 711})[0] in (403, 404)
        assert waiting.value.headers["cache-control"] == "private, no-store"

        writes_before_reload = len(writes)
        reopened_status(page, actor, "confirmed", "Confirmación recibida")
        prepare_after_confirmation(actor)
        reopened_status(page, actor, "prepared", "Preparación registrada")
        assert len(writes) == writes_before_reload
        with open_context(browser, mobile) as replacement:
            assert replacement.request.get(f"{origin}/__signup-fixture-sign-in/{actor}").status == 204
            assert replacement.request.get(origin + "/alta").status == 404
            csrf = next(cookie["value"] for cookie in replacement.cookies() if cookie["name"] == "csrftoken")
            denial = replacement.request.post(origin + "/api/v1/alta/estado",
                                              headers={"Origin": origin, "X-CSRFToken": csrf}, data={})
            assert denial.status == 404 and "QA Organización" not in denial.text() and "prepared" not in denial.text()
        discard_status_while_hidden(context, actor)
        hidden_status_after_rotation(context, actor)


def mcp_regression(browser):
    with open_context(browser) as context:
        page = context.new_page()
        sign_in(page, "staff")
        assert page.goto(origin + "/alta").status == 404
        assert page.goto(origin).status == 200
        assert post(page, "/api/v1/alta/estado", {})[0] == 404
        status, body = post(page, "/api/v1/auth/mcp-token", {"entity_id": 711, "outlet_id": 711})
        assert status == 201
        token = Path(scratch) / "mcp-fixture-token"
        with os.fdopen(os.open(token, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as stream:
            stream.write(json.loads(body)["token"] + "\n")
        try:
            env = {key: val for key, val in os.environ.items()
                   if key in ("PATH", "HOME", "LANG", "LD_LIBRARY_PATH")}
            env["SSL_CERT_FILE"] = certificate
            with subprocess.Popen([mcp, "--origin", origin, "--token-file", str(token)], env=env,
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.DEVNULL, text=True) as proc:
                seq = 0

                def rpc(method, params=None, notify=False):
                    nonlocal seq
                    seq += 1
                    request = {"jsonrpc": "2.0", "method": method, "params": params or {}}
                    if not notify:
                        request["id"] = seq
                    proc.stdin.write(json.dumps(request) + "\n")
                    proc.stdin.flush()
                    if not notify:
                        for line in proc.stdout:
                            reply = json.loads(line)
                            if reply.get("id") == seq:
                                return reply
                        raise AssertionError(f"packaged MCP exited during {method}")

                try:
                    ready = rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {},
                                               "clientInfo": {"name": "gomar-signup-qa", "version": "1"}})
                    assert "result" in ready
                    rpc("notifications/initialized", notify=True)
                    tools = rpc("tools/list")["result"]["tools"]
                    assert all(tool["annotations"]["readOnlyHint"] for tool in tools)
                    assert not any("signup" in t["name"] or "alta" in t["name"] or "consent" in t["name"]
                                   for t in tools)
                    scoped = rpc("tools/call", {"name": "gomar_list_outlets", "arguments": {"entity_id": 711}})["result"]
                    assert not scoped.get("isError") and "QA local permitido" in json.dumps(scoped)
                    assert "QA local ajeno" not in json.dumps(scoped)
                    denied = rpc("tools/call", {"name": "gomar_list_outlets", "arguments": {"entity_id": 712}})["result"]
                    assert denied.get("isError") and "structuredContent" not in denied
                    hidden = rpc("tools/call", {"name": "gomar_confirm_native_owner_signup", "arguments": {}})
                    assert "error" in hidden or hidden.get("result", {}).get("isError")
                    hidden_status = rpc("tools/call", {"name": "gomar_get_native_signup_status", "arguments": {}})
                    assert "error" in hidden_status or hidden_status.get("result", {}).get("isError")
                    assert not any(text in json.dumps(denied) + json.dumps(hidden) + json.dumps(hidden_status)
                                   for text in ("private-request", "QA Organización", "accept-"))
                finally:
                    proc.stdin.close()
                    try:
                        proc.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait()
        finally:
            token.unlink()


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(
        executable_path=chrome,
        args=["--no-sandbox", "--disable-background-networking", "--no-proxy-server",
              "--host-resolver-rules=EXCLUDE 127.0.0.1, MAP * ~NOTFOUND"],
    )
    try:
        check_native(browser, "desk", False)
        check_native(browser, "mobile", True)
        mcp_regression(browser)
    finally:
        browser.close()
print("PASS: late hidden status dropped and independently refreshed in Chrome desktop/mobile")
print("PASS: hidden-at-mount rotation denied on first visible check in Chrome desktop/mobile")
print("PASS: original-session confirmed and prepared after restart in Chrome desktop/mobile")
print("PASS: native signup desktop/mobile and packaged MCP")
