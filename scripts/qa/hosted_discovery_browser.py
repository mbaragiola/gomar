"""Real local Chrome and packaged MCP against the same isolated discovery database."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import subprocess
import sys
import time

from playwright.sync_api import sync_playwright


origin, control, binary, certificate, scratch = sys.argv[1:]
entities = "/api/v1/entities"


def classify(payload):
    data = json.dumps(payload)
    assert all(item not in data for item in ("paid-alpha", "paid-beta", "scope-branch", "verified_at", "valid_until"))
    return payload


def request(page, path, payload=None):
    result = page.evaluate("""async ([path, data]) => {
      const options = {cache: 'no-store'};
      if (data !== null) {
        const csrf = document.cookie.match(/(?:^|; )csrftoken=([^;]+)/)?.[1];
        Object.assign(options, {method:'POST', body:JSON.stringify(data),
          headers:{'Content-Type':'application/json','X-CSRFToken':csrf}});
      }
      const reply = await fetch(path, options);
      return [reply.status, await reply.json()];
    }""", [path, payload])
    return result[0], classify(result[1])


def ids(response, expected):
    status, result = response
    assert status == 200 and [item["id"] for item in result] == expected


def browser_scope(ctx, authority):
    page = ctx.new_page()
    assert page.request.get(authority + "/__discovery-fixture-sign-in").status == 204
    # Use a real authenticated Chrome page and the actual API. Do not
    # navigate the wider app or exercise any non-discovery surface.
    assert page.goto(authority + "/api/v1/auth/me").status == 200
    return page


def advance(phase):
    ready = Path(scratch) / ("ready-" + phase)
    with os.fdopen(os.open(ready, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "wb"):
        pass
    signal = Path(scratch) / ("observed-" + phase)
    until = time.monotonic() + 8
    while not signal.is_file() and time.monotonic() < until:
        time.sleep(.02)
    assert signal.is_file()


@contextmanager
def mcp_session(page, number, outlet):
    status, issued = request(page, "/api/v1/auth/mcp-token", {"entity_id": number, "outlet_id": outlet})
    assert status == 201
    token = Path(scratch) / f"fixture-token-{number}"
    with os.fdopen(os.open(token, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as stream:
        stream.write(issued["token"] + "\n")
    environ = {key: val for key, val in os.environ.items() if key in ("PATH", "HOME", "LANG", "LD_LIBRARY_PATH")}
    environ["SSL_CERT_FILE"] = certificate
    try:
        with subprocess.Popen([binary, "--origin", origin, "--token-file", str(token)], env=environ,
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                              text=True) as peer:
            count = 0

            def rpc(method, params, notification=False):
                nonlocal count
                count += 1
                msg = {"jsonrpc": "2.0", "method": method, "params": params}
                if not notification:
                    msg["id"] = count
                peer.stdin.write(json.dumps(msg) + "\n")
                peer.stdin.flush()
                if notification:
                    return
                for line in peer.stdout:
                    reply = json.loads(line)
                    if reply.get("id") == count:
                        return classify(reply)
                raise AssertionError("packaged MCP closed before the requested result")

            def call(tool, expected, specified=None):
                reply = rpc("tools/call", {"name": tool, "arguments": {} if specified is None else {"entity_id": specified}})
                result = reply["result"]
                if expected is None:
                    assert result.get("isError") and "structuredContent" not in result
                    assert all(text not in json.dumps(result) for text in ("QA A descubierto", "QA local A", "00000000701"))
                    return
                field = "entities" if specified is None else "outlets"
                assert not result.get("isError")
                assert [entry["id"] for entry in result["structuredContent"][field]] == expected

            try:
                assert "result" in rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {},
                                                      "clientInfo": {"name": "gomar-discovery-qa", "version": "1"}})
                rpc("notifications/initialized", {}, True)
                yield call
            finally:
                peer.stdin.close()
                try:
                    peer.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    peer.kill()
                    peer.wait()
    finally:
        token.unlink()


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(executable_path=os.environ.get("QA_CHROME", "/opt/google/chrome/chrome"),
        args=["--no-sandbox", "--disable-background-networking", "--no-proxy-server",
              "--host-resolver-rules=EXCLUDE 127.0.0.1, MAP * ~NOTFOUND"])
    try:
        with browser.new_context(ignore_https_errors=True) as hosted, browser.new_context(
                ignore_https_errors=True, is_mobile=True, has_touch=True,
                viewport={"width": 390, "height": 820}) as phone, browser.new_context(ignore_https_errors=True) as ordinary:
            for context in (hosted, phone, ordinary):
                context.route("**/*", lambda route: route.continue_() if route.request.url.startswith(
                    (origin + "/", control + "/")) else route.abort())
            desk = browser_scope(hosted, origin)
            mobile = browser_scope(phone, origin)
            selfhost = browser_scope(ordinary, control)
            for page in (desk, mobile):
                ids(request(page, entities), [701, 702, 703])
                ids(request(page, entities + "/701/outlets"), [711])
            with mcp_session(desk, 701, 711) as a, mcp_session(desk, 702, 721) as b:
                a("gomar_list_entities", [701])
                a("gomar_list_outlets", [711], 701)
                a("gomar_list_outlets", None, 702)
                for phase in ("suspend", "expire"):
                    advance(phase)
                    for page in (desk, mobile):
                        status, result = request(page, entities)
                        ids((status, result), [702, 703])
                        assert all(value not in json.dumps(result) for value in ("QA A descubierto", "00000000701", "QA sin permiso mapeado"))
                        assert request(page, entities + "/701/outlets")[0] == 404
                        ids(request(page, entities + "/702/outlets"), [721])
                        ids(request(page, entities + "/703/outlets"), [731])
                    a("gomar_list_entities", None)
                    a("gomar_list_outlets", None, 701)
                    b("gomar_list_entities", [702])
                    b("gomar_list_outlets", [721], 702)
                    ids(request(selfhost, entities), [701, 702, 703, 704, 705])
                    ids(request(selfhost, entities + "/701/outlets"), [712, 711])
    finally:
        browser.close()
print("PASS: actual Chrome and packaged MCP hosted discovery: old A scope denied after suspension/expiry, B and self-host preserved")
