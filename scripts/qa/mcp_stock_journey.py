"""Real Chrome + packaged MCP, on fresh local state only. No model involved."""
import contextlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import time
import urllib.request

from playwright.sync_api import expect, sync_playwright

if not __debug__:
    raise RuntimeError("Run this check without Python optimization so assertions stay enabled")

root, pg, chrome = map(Path, sys.argv[1:4])
scope = {"entity_id": 1, "outlet_id": 1}
base = "/api/v1/entities/1/outlets/1"
rename_tool = "gomar_propose_pos_rename"
minimum_tool = "gomar_propose_stock_minimum_change"
old_name, new_name = "QA Pan de campo", "QA Pan tostado"
password = secrets.token_urlsafe(24)
origin = ""
user = subprocess.check_output(["id", "-un"], text=True).strip()
env = {key: os.environ[key] for key in ("PATH", "HOME", "LD_LIBRARY_PATH", "LANG", "TZ")
       if key in os.environ}
dsn = f"host={root}/socket port=55432 user={user} dbname=gomar_qa_stock_journey sslmode=disable"
env["GOMAR_POSTGRES_DSN"] = dsn


def run(command, **kwargs):
    return subprocess.run(command, check=True, env=env, text=True, **kwargs)


def psql(sql):
    return run([pg / "psql", dsn, "-X", "-v", "ON_ERROR_STOP=1", "-Atq"],
               input=sql, capture_output=True).stdout.strip()


def seed():
    (root / "socket").mkdir(mode=0o700)
    run([pg / "initdb", "-D", root / "db", "--locale=C", "--encoding=UTF8", "--auth=trust"],
        stdout=subprocess.DEVNULL)
    with (root / "pg.log").open("w") as log:
        run([pg / "pg_ctl", "-D", root / "db", "-l", root / "pg.log", "-o",
             f"-k {root}/socket -p 55432 -c listen_addresses=''", "-w", "start"], stdout=log)
    run([pg / "createdb", "-h", root / "socket", "-p", "55432", "-U", user,
         "gomar_qa_stock_journey"])
    (root / "state").mkdir()
    app = [root / "gomar", "-state", root / "state"]
    run([*app, "-init"], stdout=subprocess.DEVNULL)
    for username in ("qa-admin", "qa-reader"):
        run([*app, "-create-admin", username], input=password + "\n", capture_output=True)
    psql("""
BEGIN;
SET search_path=gomar,public;
UPDATE users SET staff=false,superuser=false WHERE username IN ('qa-admin','qa-reader');
INSERT INTO entities(id,name,cuit,active) VALUES
 (1,'QA negocio','00000000001',true),(2,'QA otro negocio','00000000002',true);
INSERT INTO outlets(id,entity_id,name,active) VALUES
 (1,1,'QA Centro',true),(2,1,'QA Norte',true),(3,2,'QA otra sucursal',true);
INSERT INTO memberships(id,user_id,entity_id,role,active)
 SELECT 1,id,1,'administration',true FROM users WHERE username='qa-admin';
INSERT INTO memberships(id,user_id,entity_id,role,active)
 SELECT 2,id,1,'read_only',true FROM users WHERE username='qa-reader';
INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1);
INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1),(2,1,1),(3,2,1);
INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES
 (1,1,1,'QA Pan de campo',750,true),(2,1,1,'QA Galleta',550,true),(3,2,1,'QA otro local',450,true);
INSERT INTO stock_outlets VALUES(1,true),(2,true);
INSERT INTO stock_products VALUES(1,1,true,0),(1,2,true,1),(2,3,true,2);
COMMIT;
""")


def login(browser, username, mobile=False):
    context = browser.new_context(viewport={"width": 390 if mobile else 1280, "height": 900},
                                  is_mobile=mobile, has_touch=mobile, service_workers="block")
    context.route("**/*", lambda r: r.continue_() if r.request.url.startswith(origin + "/") else r.abort())
    page = context.new_page()
    page.goto(origin)
    page.get_by_label("Usuario").fill(username)
    page.get_by_label("Contraseña").fill(password)
    page.get_by_role("button", name="Ingresar", exact=True).click()
    page.get_by_label("Entidad", exact=True).select_option("1")
    page.get_by_label("Local", exact=True).select_option("1")
    return page


def issue(page, alias, action=None):
    endpoint = "mcp-proposal-sessions" if action else "mcp-token"
    body = {**scope, **({"actions": [action]} if action else {})}
    status, credential = page.evaluate("""async ([endpoint, body]) => {
      const csrf = document.cookie.match(/(?:^|; )csrftoken=([^;]+)/)?.[1];
      const reply = await fetch('/api/v1/auth/' + endpoint, {method:'POST',
        headers:{'Content-Type':'application/json','X-CSRFToken':csrf}, body:JSON.stringify(body)});
      return [reply.status, await reply.json()];
    }""", [endpoint, body])
    if status != 201:
        raise AssertionError(f"QA credential issuance failed: HTTP {status}")
    path = root / alias
    with path.open("x") as keyfile:
        keyfile.write(credential["token"] + "\n")
    assert path.stat().st_mode & 0o777 == 0o600
    return path, credential.get("id")


@contextlib.contextmanager
def connection(read, rename=None, minimum=None):
    args = [root / "gomar-mcp", "--origin", origin, "--token-file", read]
    if rename:
        args.extend(["--proposal-entity", "1", "--proposal-outlet", "1",
                     f"--proposal-lane=pos_product_rename={rename}",
                     f"--proposal-lane=stock_minimum_change={minimum}"])
    with subprocess.Popen(args, env=env, text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL) as mcp:
        seq = 0

        def rpc(method, params=None, notify=False):
            nonlocal seq
            seq += 1
            payload = {"jsonrpc": "2.0", "method": method, "params": params or {}}
            if not notify:
                payload["id"] = seq
            mcp.stdin.write(json.dumps(payload) + "\n")
            mcp.stdin.flush()
            if not notify:
                for line in mcp.stdout:
                    msg = json.loads(line)
                    if msg.get("id") == seq:
                        assert "result" in msg, f"MCP {method} failed"
                        return msg["result"]
                raise AssertionError(f"MCP ended during {method}")

        try:
            rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {},
                               "clientInfo": {"name": "gomar-stock-journey-qa", "version": "1"}})
            rpc("notifications/initialized", notify=True)
            tools = rpc("tools/list")["tools"]
            writes = {tool["name"] for tool in tools if not tool["annotations"].get("readOnlyHint")}
            assert writes == ({rename_tool, minimum_tool} if rename else set())

            def call(name, arguments, denied=False):
                result = rpc("tools/call", {"name": name, "arguments": arguments})
                assert result.get("isError", False) == denied, f"wrong permission result for {name}"
                if denied:
                    assert "structuredContent" not in result
                    assert "QA otro local" not in json.dumps(result)
                    return None
                return result["structuredContent"]

            yield call
        finally:
            mcp.stdin.close()
            try:
                mcp.wait(timeout=5)
            except subprocess.TimeoutExpired:
                mcp.kill()
                mcp.wait()


def pending(call, name, field, requested, step, branch=None, denied=False):
    result = call(name, {**scope, **(branch or {}), "request_id": f"00000000-0000-4000-8000-{step:012d}",
                         field: requested}, denied)
    if not denied:
        assert result["status"] == "pending"
        assert set(result) == {"id", "status", "expires_at"}
        return result["id"]


def readback(call, name, revision, minimum):
    product = call("gomar_read_pos_product", {**scope, "product_id": 1})
    assert product == {"id": 1, "name": name, "revision": revision, "price_minor": 750,
                       "active": True, "currency": "ARS"}
    stock = call("gomar_read_stock_product", {**scope, "product_id": 2})
    assert stock == {"id": 2, "outlet_enabled": True, "product_enabled": True,
                     "minimum": minimum, "balance": 0, "alert": "low"}
    alerts = call("gomar_list_stock", {**scope, "alert": "low"})
    assert alerts == {"enabled": True, "alert_filter": "low", "products": [
        {"id": 1, "name": name, "enabled": True, "minimum": 0, "balance": 0, "alert": "low"},
        {"id": 2, "name": "QA Galleta", "enabled": True, "minimum": minimum, "balance": 0, "alert": "low"},
    ]}


def review_panel(page):
    page.get_by_role("button", name="Chat y propuestas", exact=True).click()
    page.get_by_role("button", name="Sesiones de propuestas", exact=True).click()
    return page.get_by_role("region", name="Sesiones de propuestas")


def show(panel, proposal_id, suffix=""):
    panel.get_by_role("button", name="Actualizar sesiones y propuestas").click()
    panel.get_by_role("button", name=f"Ver propuesta {proposal_id}{suffix}", exact=True).click()
    return panel.get_by_role("region", name="Vista previa de la propuesta")


def snapshot():
    return psql("""
SELECT
 (SELECT count(*) FROM gomar.pos_product_versions),
 (SELECT current_revision FROM gomar.pos_products WHERE id=1),
 (SELECT current_revision FROM gomar.pos_products WHERE id=2),
 (SELECT current_revision FROM gomar.pos_products WHERE id=3),
 (SELECT minimum FROM gomar.stock_products WHERE outlet_id=1 AND product_id=1),
 (SELECT minimum FROM gomar.stock_products WHERE outlet_id=1 AND product_id=2),
 (SELECT minimum FROM gomar.stock_products WHERE outlet_id=2 AND product_id=3),
 (SELECT count(*) FROM gomar.stock_movements),
 (SELECT count(*) FROM gomar.pos_sales);
""")


def journey(chromium):
    owner = login(chromium, "qa-admin")
    peer = login(chromium, "qa-admin", mobile=True)
    reader = login(chromium, "qa-reader")
    read, _ = issue(owner, "read")
    rename, rename_session = issue(owner, "rename", "pos_product_rename")
    minimum, min_session = issue(owner, "minimum", "stock_minimum_change")
    restricted, _ = issue(reader, "restricted")
    assert rename_session != min_session
    rename_input = {"product_id": 1, "expected_revision": 1, "name": new_name}
    min_input = {"product_id": 2, "expected_revision": 1, "minimum": 5}
    original = "3|1|1|1|0|1|2|0|0"
    assert snapshot() == original
    with connection(read, rename, minimum) as call:
        readback(call, old_name, 1, 1)
        rename_id = pending(call, rename_tool, "rename", rename_input, 1)
        reject_id = pending(call, minimum_tool, "minimum_change", min_input, 2)
        pending(call, rename_tool, "rename", rename_input, 3, {"outlet_id": 2}, denied=True)
        pending(call, minimum_tool, "minimum_change", min_input, 4, {"entity_id": 2}, denied=True)
        call("gomar_read_stock_product", {**scope, "outlet_id": 2, "product_id": 3}, denied=True)
        with connection(restricted) as readonly:
            readonly("gomar_read_stock_product", {**scope, "product_id": 2}, denied=True)
        readback(call, old_name, 1, 1)
        assert snapshot() == original

        # A different browser login by the same actor cannot see these receipts.
        peer_status = peer.evaluate("""async (urls) => Promise.all(
          urls.map(async (url) => (await fetch(url)).status))""",
          [f"{base}/pos-rename-proposals/{rename_id}", f"{base}/stock-minimum-proposals/{reject_id}"])
        assert peer_status == [404, 404]
        peer_decisions = peer.evaluate("""async (urls) => {
          const csrf = document.cookie.match(/(?:^|; )csrftoken=([^;]+)/)?.[1];
          const headers = {'Content-Type':'application/json','X-CSRFToken':csrf};
          const body = JSON.stringify({decision:'reject', digest:'0'.repeat(64), confirm:true});
          return Promise.all(urls.map(async url => (await fetch(url + '/decision',
            {method:'POST', headers, body})).status));
        }""", [f"{base}/pos-rename-proposals/{rename_id}", f"{base}/stock-minimum-proposals/{reject_id}"])
        assert peer_decisions == [404, 404]
        assert snapshot() == original
        panel = review_panel(owner)
        preview = show(panel, rename_id)
        expect(preview.get_by_label("Nombre actual", exact=True)).to_have_text(old_name)
        expect(preview.get_by_label("Nombre propuesto", exact=True)).to_have_text(new_name)
        preview.get_by_role("button", name="Continuar con este cambio").click()
        readback(call, old_name, 1, 1)
        preview.get_by_role("button", name="Confirmar cambio de nombre").click()
        expect(panel.get_by_role("status")).to_contain_text("Nombre del producto actualizado.")
        readback(call, new_name, 2, 1)

        preview = show(panel, reject_id, " de mínimo de stock")
        expect(preview).to_contain_text("Mínimo actual: 1 unidades")
        expect(preview).to_contain_text("Mínimo propuesto: 5 unidades")
        preview.get_by_role("button", name="Rechazar propuesta").click()
        preview.get_by_role("button", name="Sí, rechazar").click()
        expect(panel.get_by_role("status")).to_contain_text("Propuesta rechazada. No se cambió el mínimo.")
        readback(call, new_name, 2, 1)
        assert snapshot() == "4|2|1|1|0|1|2|0|0"

        # Revoking the rename credential does not stop minimum or read calls
        # in the same persistent stdio connection.
        panel.get_by_role("button", name=f"Revocar sesión {rename_session}", exact=True).click()
        expect(panel.get_by_role("button", name=f"Revocar sesión {rename_session}", exact=True)).to_have_count(0)
        expect(panel.get_by_role("button", name=f"Revocar sesión {min_session}", exact=True)).to_be_visible()
        pending(call, rename_tool, "rename", {**rename_input, "expected_revision": 2}, 5, denied=True)
        approve_id = pending(call, minimum_tool, "minimum_change", min_input, 6)
        readback(call, new_name, 2, 1)
        preview = show(panel, approve_id, " de mínimo de stock")
        preview.get_by_role("button", name="Continuar con el mínimo de stock").click()
        readback(call, new_name, 2, 1)
        preview.get_by_role("button", name="Confirmar nuevo mínimo").click()
        expect(panel.get_by_role("status")).to_contain_text("Mínimo de stock actualizado.")
        readback(call, new_name, 2, 5)
        assert snapshot() == "4|2|1|1|0|5|2|0|0"
        assert psql("""
SELECT
 (SELECT count(*) FROM gomar.mcp_pos_rename_proposals),
 (SELECT count(*) FROM gomar.mcp_stock_minimum_proposals),
 (SELECT count(*) FROM gomar.mcp_pos_rename_proposals WHERE status='approved'),
 (SELECT count(*) FROM gomar.mcp_stock_minimum_proposals WHERE status='rejected'),
 (SELECT count(*) FROM gomar.mcp_stock_minimum_proposals WHERE status='approved'),
 (SELECT count(*) FROM gomar.mcp_proposal_sessions WHERE revoked_at IS NOT NULL);
""") == "1|2|1|1|1|1"


def main():
    global origin
    seed()
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        address = f"127.0.0.1:{listener.getsockname()[1]}"
    origin = "http://" + address
    with (root / "native.log").open("w") as log:
        server = subprocess.Popen([root / "gomar", "-state", root / "state", "-listen", address,
                                   "-enable-mcp", "-enable-pos", "-enable-stock",
                                   "-enable-mcp-proposal-sessions", "-enable-mcp-stock-minimum-proposals"],
                                  env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            for _ in range(100):
                try:
                    with urllib.request.urlopen(origin + "/healthz", timeout=0.2):
                        break
                except OSError:
                    if server.poll() is not None:
                        raise AssertionError("Local Gomar exited")
                    time.sleep(0.1)
            else:
                raise AssertionError("Local Gomar did not become ready")
            with sync_playwright() as p:
                browser = p.chromium.launch(
                    executable_path=str(chrome),
                    args=["--no-sandbox", "--disable-background-networking", "--no-proxy-server",
                          "--host-resolver-rules=EXCLUDE 127.0.0.1, MAP * ~NOTFOUND"])
                try:
                    journey(browser)
                finally:
                    browser.close()
        finally:
            server.terminate()
            server.wait(timeout=8)
    print("PASS: one packaged MCP session, Chrome review, denials, revocation, and exact stock readback")


if __name__ == "__main__":
    main()
