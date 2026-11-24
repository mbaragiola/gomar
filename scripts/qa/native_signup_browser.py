"""Exact synthetic Gomar HTTPS routes in desktop/mobile Chrome and packaged MCP."""
import json
import os
from pathlib import Path
import stat
import subprocess
import sys

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
        expect(page.get_by_role("heading", name=f"QA Organización {actor}")).to_be_focused()
        expect(page.get_by_role("list", name="Locales incluidos").locator("li")).to_have_text(["Centro", "Norte"])
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


def mcp_regression(browser):
    with open_context(browser) as context:
        page = context.new_page()
        sign_in(page, "staff")
        assert page.goto(origin + "/alta").status == 404
        assert page.goto(origin).status == 200
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
                    assert not any(text in json.dumps(denied) + json.dumps(hidden)
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
print("PASS: native signup desktop/mobile and packaged MCP")
