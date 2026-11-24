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

from codex_stock_transport import codex_tools
from mcp_purchase_checks import purchase_checks, purchase_seed, purchase_snapshot, purchase_tools
from mcp_treasury_checks import treasury_checks, treasury_seed, treasury_snapshot, treasury_tools
from pos_prebill_checks import sale_count

if not __debug__:
    raise RuntimeError("Run this check without Python optimization so assertions stay enabled")

root, pg, chrome = map(Path, sys.argv[1:4])
codex = Path(sys.argv[4]) if sys.argv[4] else None
with_purchases = sys.argv[5] == "true"
with_bookings_wiki = sys.argv[6] == "true"
with_barcode = len(sys.argv) > 8 and sys.argv[8] == "true"
if with_bookings_wiki:
    from mcp_bookings_wiki_checks import booking_checks, booking_seed, booking_tools, confirmation_tool
with_pos_prebill = len(sys.argv) > 7 and sys.argv[7] == "true"
if with_pos_prebill:
    from pos_prebill_checks import check_prebill
with_pos_table_checks = len(sys.argv) > 9 and sys.argv[9] == "true"
if with_pos_table_checks:
    from pos_table_checks import check_saved_table
with_manual_tender = len(sys.argv) > 10 and sys.argv[10] == "true"
if with_manual_tender:
    from pos_manual_tender_checks import check_manual_tender
with_finalization = len(sys.argv) > 11 and sys.argv[11] == "true"
if with_finalization:
    from pos_table_finalization_checks import check_finalization

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
    if with_purchases:
        purchase_seed(psql)
        treasury_seed(psql)
    if with_bookings_wiki:
        booking_seed(psql)
    if with_pos_prebill or with_pos_table_checks or with_manual_tender or with_finalization:
        run([*app, "-create-admin", "qa-pos-limited"], input=password + "\n", capture_output=True)
        psql("""
BEGIN;
UPDATE gomar.users SET staff=false,superuser=false WHERE username='qa-pos-limited';
INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
 SELECT 3,id,1,'administration',true FROM gomar.users WHERE username='qa-pos-limited';
INSERT INTO gomar.outlet_grants VALUES(3,1);
COMMIT;
""")


def login(browser, username, mobile=False, allow_pos_worker=False):
    context = browser.new_context(viewport={"width": 390 if mobile else 1280, "height": 900},
                                  is_mobile=mobile, has_touch=mobile,
                                  service_workers="allow" if allow_pos_worker else "block")
    context.route("**/*", lambda r: r.continue_() if r.request.url.startswith(origin + "/") else r.abort())
    page = context.new_page()
    page.goto(origin)
    page.get_by_label("Usuario").fill(username)
    page.get_by_label("Contraseña").fill(password)
    page.get_by_role("button", name="Ingresar", exact=True).click()
    page.get_by_label("Entidad", exact=True).select_option("1")
    page.get_by_label("Local", exact=True).select_option("1")
    return page


def request_credential(page, action=None):
    endpoint = "mcp-proposal-sessions" if action else "mcp-token"
    body = {**scope, **({"actions": [action]} if action else {})}
    return page.evaluate("""async ([endpoint, body]) => {
      const csrf = document.cookie.match(/(?:^|; )csrftoken=([^;]+)/)?.[1];
      const reply = await fetch('/api/v1/auth/' + endpoint, {method:'POST',
        headers:{'Content-Type':'application/json','X-CSRFToken':csrf}, body:JSON.stringify(body)});
      return [reply.status, await reply.json()];
    }""", [endpoint, body])


def issue(page, alias, action=None):
    status, credential = request_credential(page, action)
    if status != 201:
        raise AssertionError(f"QA credential issuance failed: HTTP {status}")
    path = root / alias
    with path.open("x") as keyfile:
        keyfile.write(credential["token"] + "\n")
    assert path.stat().st_mode & 0o777 == 0o600
    return path, credential.get("id")


def tool_caller(tools, invoke, expected_writes):
    writes = {tool["name"] for tool in tools if not tool["annotations"].get("readOnlyHint")}
    assert writes == expected_writes
    reads = {"gomar_read_pos_product", "gomar_read_stock_product", "gomar_list_stock"}
    assert reads.issubset({tool["name"] for tool in tools})

    def call(name, arguments, denied=False):
        result = invoke(name, arguments)
        assert result.get("isError", False) == denied, f"wrong permission result for {name}"
        if denied:
            assert "structuredContent" not in result
            assert all(sentinel not in json.dumps(result) for sentinel in ("QA otro local", "qa-private", "qa-booking-hidden"))
            return None
        return result["structuredContent"]

    return call


@contextlib.contextmanager
def connection(read, rename=None, minimum=None, booking=None, barcode=None):
    args = [root / "gomar-mcp", "--origin", origin, "--token-file", read]
    writes = set()
    if rename:
        writes = {rename_tool, minimum_tool}
        args.extend(["--proposal-entity", "1", "--proposal-outlet", "1",
                     f"--proposal-lane=pos_product_rename={rename}",
                     f"--proposal-lane=stock_minimum_change={minimum}"])
    if booking:
        assert not rename and not minimum
        writes = {confirmation_tool}
        args.extend(["--proposal-session-token-file", booking, "--proposal-session-action", "reservation_confirm"])
    if barcode:
        assert not rename and not minimum and not booking
        writes = {"gomar_propose_pos_barcode"}
        args.extend(["--proposal-session-token-file", barcode, "--proposal-session-action", "pos_product_barcode_change"])
    if codex:
        with codex_tools(codex, args, root) as (tools, invoke):
            yield tool_caller(tools, invoke, writes)
        return
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
            yield tool_caller(tools, lambda name, arguments: rpc(
                "tools/call", {"name": name, "arguments": arguments}), writes)
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
    original_purchases = purchase_snapshot(psql) if with_purchases else None
    original_treasury = treasury_snapshot(psql) if with_purchases else None
    with connection(read, rename, minimum) as call:
        readback(call, old_name, 1, 1)
        rename_id = pending(call, rename_tool, "rename", rename_input, 1)
        reject_id = pending(call, minimum_tool, "minimum_change", min_input, 2)
        pending(call, rename_tool, "rename", rename_input, 3, {"outlet_id": 2}, denied=True)
        pending(call, minimum_tool, "minimum_change", min_input, 4, {"entity_id": 2}, denied=True)
        call("gomar_read_stock_product", {**scope, "outlet_id": 2, "product_id": 3}, denied=True)
        with connection(restricted) as readonly:
            readonly("gomar_read_stock_product", {**scope, "product_id": 2}, denied=True)
            if with_purchases:
                purchase_tools(readonly, scope, may_handoff=False)
                treasury_tools(readonly, scope, allowed=False)
            if with_bookings_wiki:
                booking_tools(readonly, scope, allowed=False)
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
        if with_purchases:
            purchase_checks(call, owner, reader, origin, scope)
            treasury_tools(call, scope, allowed=True)
            owner.goto(origin)
            treasury_checks(owner, origin)
            treasury_checks(peer, origin)
            assert purchase_snapshot(psql) == original_purchases
            assert treasury_snapshot(psql) == original_treasury

    if with_bookings_wiki:
        disabled, denied = request_credential(owner, "reservation_cancel"), request_credential(reader, "reservation_confirm")
        assert disabled[0] == 422 and "token" not in disabled[1]
        assert denied[0] == 403 and "token" not in denied[1]
        booking, _ = issue(owner, "booking", "reservation_confirm")
        with connection(read, booking=booking) as scoped_booking:
            booking_checks(scoped_booking, owner, peer, origin, scope, psql, review_panel, show)
    else:
        status, denied = request_credential(owner, "reservation_confirm")
        assert status == 422 and "token" not in denied



def barcode_journey(browser):
    # The fixture has only loopback. Drive the browser's connectivity hint
    # independently while Playwright also removes actual network access.
    online_hint = "Object.defineProperty(Navigator.prototype, 'onLine', {get: () => !window.qaOffline})"
    owner = login(browser, "qa-admin", allow_pos_worker=True)
    owner.add_init_script(online_hint)
    owner.goto(origin + "/pos")
    local = owner.get_by_role("combobox", name="Local", exact=True)
    expect(local).to_have_value("1")
    expect(local.locator("option:checked")).to_have_text("QA Centro")
    owner.get_by_text("Gestionar códigos de barras", exact=True).click()
    product = owner.get_by_label("Producto para asignar código")
    product.select_option("1")
    previous, new = "000719", "000720"
    owner.get_by_label("Código de barras del producto").fill(previous)
    owner.get_by_role("button", name="Guardar código de barras").click()
    expect(owner.get_by_text("Código de barras guardado.", exact=True)).to_be_visible()
    owner.goto(origin)
    owner.get_by_label("Entidad", exact=True).select_option("1")
    owner.get_by_label("Local", exact=True).select_option("1")
    read, _ = issue(owner, "barcode-read")
    key, _ = issue(owner, "barcode-proposal", "pos_product_barcode_change")
    with connection(read, barcode=key) as call:
        def find(code):
            return call("gomar_pos_catalog", {**scope, "barcode": code})["products"]

        match = find(previous)
        assert len(match) == 1 and match[0]["id"] == 1 and match[0]["barcode"] == previous
        assert not find(previous.lstrip("0"))
        call("gomar_pos_catalog", {**scope, "outlet_id": 2, "barcode": previous}, denied=True)
        proposal = pending(call, "gomar_propose_pos_barcode", "change",
                           {"product_id": 1, "expected_revision": match[0]["revision"], "barcode": new}, 93)
        assert find(previous) and not find(new)
        panel = review_panel(owner)
        preview = show(panel, proposal, " de código de barras")
        expect(preview).to_contain_text(previous)
        expect(preview).to_contain_text(new)
        preview.get_by_role("button", name="Continuar con el código de barras").click()
        assert find(previous) and not find(new)
        preview.get_by_role("button", name="Confirmar código de barras").click()
        expect(panel.get_by_role("status")).to_contain_text("Código de barras")
        assert not find(previous) and find(new)[0]["id"] == 1

    for mobile in (False, True):
        register = login(browser, "qa-admin", mobile=mobile, allow_pos_worker=True)
        register.add_init_script(online_hint)
        register.goto(origin + "/pos")
        local = register.get_by_role("combobox", name="Local", exact=True)
        expect(local).to_have_value("1")
        expect(local.locator("option:checked")).to_have_text("QA Centro")
        register.get_by_role("button", name="Habilitar caja en esta pestaña").click()
        scan = register.get_by_label("Código de barras", exact=True)
        name = "QA Pan tostado"
        def scan_code(code):
            scan.fill("")
            scan.press_sequentially(code)
            scan.press("Enter")

        scan_code(new)
        expect(register.get_by_text(f"{name} × 1", exact=True)).to_be_visible()
        scan_code(new)
        expect(register.get_by_text(f"{name} × 2", exact=True)).to_be_visible()
        register.get_by_role("button", name="Ver cuenta de mesa").click()
        check = register.get_by_role("region", name="Cuenta de mesa")
        expect(check.get_by_text(f"2 × {name}", exact=True)).to_be_visible()
        register.get_by_role("button", name="Volver a la venta").click()
        scan_code(new.lstrip("0"))
        expect(register.get_by_role("alert")).to_contain_text("código")
        expect(register.get_by_text(f"{name} × 2", exact=True)).to_be_visible()
        register.context.set_offline(True)
        register.evaluate("window.qaOffline = true; window.dispatchEvent(new Event('offline'))")
        expect(register.get_by_text("Sin conexión", exact=True)).to_be_visible()
        scan_code(new)
        expect(register.get_by_text(f"{name} × 3", exact=True)).to_be_visible()
        register.get_by_role("button", name="Bloquear caja").click()
        expect(scan).to_have_count(0)
        assert sale_count(register) == 0
        register.context.close()
    assert psql("SELECT count(*) FROM gomar.pos_sales") == "0"
    owner.context.close()


def main():
    global origin
    seed()
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        address = f"127.0.0.1:{listener.getsockname()[1]}"
    origin = "http://" + address
    app_flags = ["-enable-mcp", "-enable-pos", "-enable-stock", "-enable-mcp-proposal-sessions",
                 "-enable-mcp-stock-minimum-proposals"]
    if with_bookings_wiki:
        app_flags.extend(["-public-reservations", "-enable-mcp-reservation-confirm-proposals"])
    if with_barcode:
        app_flags.append("-enable-mcp-pos-catalog-proposals")
    if with_pos_table_checks or with_manual_tender or with_finalization:
        app_flags.append("-enable-pos-table-checks")
    if with_manual_tender or with_finalization:
        app_flags.append("-enable-pos-manual-tender")
    if with_finalization:
        app_flags.append("-enable-pos-table-finalization")
    with (root / "native.log").open("w") as log:
        server = subprocess.Popen([root / "gomar", "-state", root / "state", "-listen", address, *app_flags],
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
                    if with_pos_prebill:
                        before = snapshot()
                        check_prebill(browser, login, origin)
                        assert snapshot() == before
                    if with_pos_table_checks:
                        check_saved_table(browser, login, origin, psql, issue, connection)
                    if with_manual_tender:
                        check_manual_tender(browser, login, origin, psql, issue, connection, with_finalization)
                    journey(browser)
                    if with_barcode:
                        barcode_journey(browser)
                    if with_finalization:
                        check_finalization(browser, login, origin, psql, issue, connection)
                finally:
                    browser.close()
        finally:
            server.terminate()
            server.wait(timeout=8)
    if with_barcode:
        print("PASS: exact barcode lookup and native approval in MCP; desktop/mobile Chrome scans, offline cache, and no sale")
    if with_manual_tender:
        print("PASS: manual tender desktop/mobile, native corrections, MCP status/handoff and denials; no sale or funds movement")
    transport = "one installed Codex dual-lane session" if codex else "one packaged MCP session"
    print(f"PASS: {transport}, Chrome review, denials, revocation, and exact stock readback")
    if with_purchases:
        print("PASS: same staff session, scoped purchases and native handoff; no changes to documents or payments")
        print("PASS: desktop/mobile treasury outlet switches, scoped MCP payment reads and denials; no payment writes")

    if with_bookings_wiki:
        print("PASS: scoped reservation proposal, native approval, wiki reads, and role/branch/action denials")
    if with_pos_prebill:
        print("PASS: native POS table checks on desktop and mobile, 80 mm and 58 mm PDF, handoff denials, no sale or payment writes")
    if with_pos_table_checks:
        print("PASS: saved untendered table check, desktop and mobile handoff, packaged MCP scope denials, no sales/stock/payment writes")



if __name__ == "__main__":
    main()
