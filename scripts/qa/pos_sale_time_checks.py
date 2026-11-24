"""Chrome's actual POS offline queue and packaged MCP time evidence, on synthetic local state."""
from datetime import datetime, timedelta, timezone
import time
from uuid import UUID
from zoneinfo import ZoneInfo

from playwright.sync_api import expect

from pos_prebill_checks import use_fixture_connection

TOOL = "gomar_read_pos_sale_time"
SCOPE = {"entity_id": 1, "outlet_id": 1}
LOCAL = "/api/v1/entities/1/outlets/1/pos"


def check_pos_sale_time(browser, login, origin, psql, issue, connection):
    user = login(browser, "qa-pos-limited", allow_pos_worker=True)
    other = login(browser, "qa-admin")
    reader = login(browser, "qa-reader")
    key, _ = issue(user, "sale-time-owner")
    other_key, _ = issue(other, "sale-time-other")
    reader_key, _ = issue(reader, "sale-time-reader")
    try:
        _exercise(browser, user, other, reader, origin, psql, connection, key, other_key, reader_key)
    finally:
        user.context.close()
        other.context.close()
        reader.context.close()


def _exercise(browser, page, other, reader, origin, psql, connection, key, other_key, reader_key):
    def snapshot():
        return psql("""SELECT (SELECT count(*) FROM gomar.pos_sales),
 (SELECT count(*) FROM gomar.pos_sale_items),
 (SELECT count(*) FROM gomar.stock_movements WHERE kind='venta'),
 (SELECT count(*) FROM gomar.ap_payments);""")

    before = [int(n) for n in snapshot().split("|")]
    observed = []
    responses = []

    def lose_first_response(route):
        sent = route.request.post_data_json
        assert route.request.method == "POST"
        observed.append(sent)
        response = route.fetch(timeout=5000, max_redirects=0)
        responses.append((response.status, response.json()))
        if len(observed) == 1:
            route.abort("connectionreset")
        else:
            route.fulfill(response=response)

    # Change only Date.now in this disposable page, not the host, JavaScript
    # Date constructor, server clock, or any credential lifetime.
    clock = int(time.time() * 1000) - 9 * 60 * 1000
    # The read-only handoff opens a new tab. Apply the loopback-only hint
    # to the whole synthetic context, not only the initiating page.
    page.context.add_init_script("""Object.defineProperty(Navigator.prototype, 'onLine',
      {get: () => sessionStorage.getItem('qaOffline') !== '1'})""")
    page.goto(origin + "/pos")
    page.get_by_role("button", name="Habilitar caja en esta pestaña").click()
    expect(page.get_by_text("Caja habilitada en esta pestaña por hasta 8 horas.")).to_be_visible()
    assert page.request.get(origin + LOCAL + "/catalog").json()["hora_informada_caja_utc_v1"] is True
    page.context.set_offline(True)
    page.evaluate("""clock => {
      sessionStorage.setItem('qaOffline','1');
      Date.now = () => clock;
      window.dispatchEvent(new Event('offline'));
    }""", clock)
    expect(page.get_by_text("Sin conexión", exact=True)).to_be_visible()
    page.get_by_role("button", name="Agregar QA Galleta", exact=True).click()
    page.get_by_label("Medio de pago").select_option("efectivo")
    page.get_by_role("button", name="Registrar venta", exact=True).click()
    page.get_by_role("button", name="Confirmar y guardar venta").click()
    expect(page.get_by_text("Pendiente de sincronizar", exact=False)).to_be_visible()
    row = page.get_by_text("Hora informada por la caja (no verificada):", exact=False)
    expect(row).to_be_visible()
    claim = row.locator("time").get_attribute("datetime")
    assert round(datetime.fromisoformat(claim).timestamp() * 1000) == clock
    assert not observed and snapshot() == "|".join(map(str, before))

    # A local reload must use the same cached/offline queue, still without
    # touching the app server.
    page.reload(wait_until="domcontentloaded")
    expect(page.get_by_text("Pendiente de sincronizar", exact=False)).to_be_visible()
    row = page.get_by_text("Hora informada por la caja (no verificada):", exact=False)
    expect(row.locator("time")).to_have_attribute("datetime", claim)
    assert snapshot() == "|".join(map(str, before))

    queued_id = page.evaluate("""() => new Promise((resolve, reject) => {
      const open = indexedDB.open('gomar-pos-v1');
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const db = open.result;
        const rows = db.transaction('sales').objectStore('sales').getAll();
        rows.onerror = () => { db.close(); reject(rows.error) };
        rows.onsuccess = () => {
          db.close();
          const pending = rows.result.filter(sale => sale.status === 'pending');
          resolve(pending.length === 1 ? pending[0].id : null);
        };
      };
    })""")
    assert UUID(queued_id).version == 4 and str(UUID(queued_id)) == queued_id
    assert page.evaluate("!!sessionStorage.getItem('gomar-pos-active-lease')")

    # The active same-tab queue would auto-submit on an ordinary /pos load.
    # Malformed and canonical read-only handoffs must never mount that register.
    page.route("**/pos/sales", lose_first_response)
    handoff_requests = []
    page.on("request", lambda r: handoff_requests.append(r.method) if r.url.startswith(origin + "/api/") else None)
    page.evaluate("location.hash = '#entity=1&outlet=1&venta=malformada'")
    invalid = page.get_by_role("heading", name="Enlace de horarios no válido")
    expect(invalid).to_be_visible()
    page.context.set_offline(False)
    page.evaluate("sessionStorage.removeItem('qaOffline'); window.dispatchEvent(new Event('online'))")
    exact = f"#entity=1&outlet=1&venta={queued_id}"
    page.goto(origin + "/pos?unexpected=1" + exact)
    expect(invalid).to_be_visible()
    expect(page.get_by_role("button", name="Consultar horarios")).to_have_count(0)
    page.goto(origin + "/pos" + exact)
    expect(page.get_by_role("heading", name="Horarios de la venta de Caja")).to_be_visible()
    expect(page.get_by_role("button", name="Consultar horarios")).to_be_enabled()
    assert not handoff_requests and not observed
    assert snapshot() == "|".join(map(str, before))

    page.goto(origin + "/pos")
    retry = page.get_by_role("button", name="Reintentar conexión")
    expect(retry).to_be_visible()
    assert len(responses) == 1 and responses[0][0] == 201, responses
    assert observed[0]["hora_informada_caja_utc"] == claim
    retry.click()
    expect(page.get_by_text("Sincronizada", exact=False)).to_be_visible()
    assert len(responses) == 2 and responses[1][0] == 200, responses
    assert observed[1] == observed[0] and responses[0][1] == responses[1][1]
    registered = responses[0][1]["creado_en"]
    assert registered != claim
    assert datetime.fromisoformat(registered).timestamp() * 1000 > clock
    rid = observed[0]["id_solicitud"]
    assert rid == queued_id
    after = "|".join(map(str, [before[0]+1, before[1]+1, before[2]+1, before[3]]))
    assert snapshot() == after
    assert psql(f"SELECT count(*) FROM gomar.stock_movements m JOIN gomar.pos_sales s ON s.id=m.sale_id WHERE s.request_id='{rid}' AND m.delta=-1 AND m.recorded_at=s.recorded_at") == "1"

    target = {**SCOPE, "request_id": rid}
    with connection(key) as call:
        evidence = call(TOOL, target)
        assert evidence == {
            "request_id": rid, "registered_at_utc": registered, "till_reported_at_utc": claim,
            "till_time_status": "unverified", "handoff_url": f"/pos#entity=1&outlet=1&venta={rid}",
        }, evidence
        call(TOOL, {**target, "outlet_id": 2}, denied=True)
        call(TOOL, {**target, "entity_id": 2}, denied=True)
    with connection(other_key) as call:
        call(TOOL, target, denied=True)
    with connection(reader_key) as call:
        call(TOOL, target, denied=True)

    page.get_by_role("button", name="Agregar QA Galleta", exact=True).click()
    cart_line = page.get_by_text("QA Galleta × 1", exact=True)
    expect(cart_line).to_be_visible()
    requested = []
    page.context.on("request", lambda r: requested.append(r.method)
                    if r.url.startswith(origin + "/api/") else None)
    with page.expect_popup() as opened:
        page.get_by_role("link", name=rid, exact=True).click()
    handoff_page = opened.value
    try:
        expect(handoff_page.get_by_role("heading", name="Horarios de la venta de Caja")).to_be_visible()
        assert not requested
        handoff_page.get_by_role("button", name="Consultar horarios").click()
        expect(handoff_page.locator("time")).to_have_count(2)
        assert handoff_page.locator("time").evaluate_all("(times) => times.map(t => t.dateTime)") == [registered, claim]
        assert requested == ["GET"]
        assert handoff_page.request.get(origin + LOCAL + "/sale-times/" + rid).json() == evidence
    finally:
        handoff_page.close()
    expect(cart_line).to_be_visible()

    for denied in (other, reader):
        use_fixture_connection(denied)
        denied.goto(origin + evidence["handoff_url"])
        denied.get_by_role("button", name="Consultar horarios").click()
        expect(denied.get_by_role("alert")).to_contain_text("No se pudo consultar")
        expect(denied.locator("time")).to_have_count(0)
    offline_only = browser.new_page()
    try:
        use_fixture_connection(offline_only)
        offline_only.goto(origin + evidence["handoff_url"])
        offline_only.get_by_role("button", name="Consultar horarios").click()
        expect(offline_only.get_by_role("alert")).to_contain_text("No se pudo consultar")
        expect(offline_only.locator("time")).to_have_count(0)
    finally:
        offline_only.close()
    assert snapshot() == after


def check_pos_preclose(browser, login, origin, psql):
    """Passive native review across desktop and mobile, with a real synced receipt."""
    for mobile in (False, True):
        cashier = login(browser, "qa-pos-limited", mobile=mobile, allow_pos_worker=True)
        try:
            _preclose(cashier, origin, psql)
        finally:
            cashier.context.close()


def _preclose(page, origin, psql):
    page.context.add_init_script("""Object.defineProperty(Navigator.prototype, 'onLine',
      {get: () => sessionStorage.getItem('qaOffline') !== '1'})""")
    page.goto(origin + "/pos")
    page.get_by_role("button", name="Habilitar caja en esta pestaña").click()
    expect(page.get_by_text("Caja habilitada en esta pestaña por hasta 8 horas.")).to_be_visible()
    panel = page.get_by_role("region", name="Antes del cierre")
    inspect = panel.get_by_role("button", name="Consultar ventas de este navegador")
    same_browser_without_lease = page.context.new_page()
    try:
        same_browser_without_lease.goto(origin + "/pos")
        expect(same_browser_without_lease.get_by_role("button", name="Habilitar caja en esta pestaña")).to_be_enabled()
        expect(same_browser_without_lease.get_by_role("region", name="Antes del cierre")).to_have_count(0)
    finally:
        same_browser_without_lease.close()

    def connection_state(offline):
        page.context.set_offline(offline)
        page.evaluate("""offline => {
          if (offline) sessionStorage.setItem('qaOffline', '1');
          else sessionStorage.removeItem('qaOffline');
          window.dispatchEvent(new Event(offline ? 'offline' : 'online'));
        }""", offline)

    def sale():
        page.get_by_role("button", name="Agregar QA Galleta", exact=True).click()
        page.get_by_label("Medio de pago").select_option("efectivo")
        page.get_by_role("button", name="Registrar venta", exact=True).click()
        page.get_by_role("button", name="Confirmar y guardar venta").click()

    connection_state(True)
    expect(page.get_by_text("Sin conexión", exact=True)).to_be_visible()
    current_month = datetime.now(ZoneInfo("America/Argentina/Buenos_Aires")).replace(
        day=1, hour=0, minute=0, second=0, microsecond=0)
    last_month = current_month - timedelta(milliseconds=1)
    # Shift only the page clock for the first real enqueue. Gomar's own clock
    # and the actual credential lifetime still reflect current time.
    millis = round(last_month.timestamp() * 1000)
    page.evaluate("ms => { window.qaRealClock = Date.now; Date.now = () => ms }", millis)
    sale()
    expect(page.get_by_text("Pendiente de sincronizar", exact=False)).to_be_visible()
    page.evaluate("Date.now = window.qaRealClock")
    connection_state(False)
    expect(page.get_by_text("Sincronizada", exact=False)).to_be_visible()
    expect(inspect).to_be_enabled()
    connection_state(True)
    expect(page.get_by_text("Sin conexión", exact=True)).to_be_visible()
    sale()
    expect(page.get_by_text("Pendiente de sincronizar", exact=False)).to_be_visible()

    # Synthetic cache fixtures emulate a prior review, legacy missing evidence,
    # and another register scope without posting any of them to Gomar.
    set_fixtures = """() => new Promise((resolve, reject) => {
      const open = indexedDB.open('gomar-pos-v1');
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const db = open.result;
        const tx = db.transaction('sales', 'readwrite');
        const store = tx.objectStore('sales');
        const all = store.getAll();
        let receipt;
        all.onsuccess = () => {
          receipt = all.result.find(row => row.status === 'synced');
          const { registeredAt } = receipt.receiptClock;
          store.add({ ...receipt, id: 'QA-REVISAR', status: 'attention', receiptClock: undefined });
          store.add({ ...receipt, id: 'QA-SIN-REGISTRO', receiptClock: undefined });
          store.add({ ...receipt, id: 'QA-SIN-HORA', receiptClock: { registeredAt, status: 'not_reported' } });
          store.add({ ...receipt, id: 'QA-OTRO-LOCAL', outlet: 2, key: `${receipt.actor}:${receipt.entity}:2`, status: 'pending' });
          store.add({ ...receipt, id: 'QA-OTRA-CUENTA', actor: 99, key: `99:${receipt.entity}:${receipt.outlet}`, status: 'attention' });
        };
        tx.oncomplete = () => { db.close(); resolve(receipt); };
        tx.onabort = () => { db.close(); reject(tx.error); };
      };
    })"""
    receipt = page.evaluate(set_fixtures)
    claim = last_month.astimezone(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
    registered_at = receipt["receiptClock"]["registeredAt"]
    assert receipt["receiptClock"]["tillReportedAt"] == claim

    def snapshot():
        return psql("""SELECT (SELECT count(*) FROM gomar.pos_sales),
 (SELECT count(*) FROM gomar.pos_sale_items),
 (SELECT count(*) FROM gomar.stock_movements WHERE kind='venta'),
 (SELECT count(*) FROM gomar.ap_payments);""")

    before = snapshot()
    api = []
    page.on("request", lambda req: api.append(req.method) if req.url.startswith(origin + "/api/") else None)
    page.evaluate("""() => {
      const transaction = IDBDatabase.prototype.transaction;
      window.qaWriteTransactions = 0;
      IDBDatabase.prototype.transaction = function(stores, mode, options) {
        if (mode === 'readwrite') window.qaWriteTransactions++;
        return transaction.call(this, stores, mode, options);
      };
    }""")
    expect(panel.get_by_text("Otras cajas sin verificar.")).to_be_visible()
    expect(panel.get_by_role("heading", name="Pendientes o para revisar")).to_have_count(0)
    inspect.click()
    expect(panel.get_by_text("Pendientes de sincronizar: 1. Necesitan revisión: 1.")).to_be_visible()
    expect(panel.get_by_text("QA-REVISAR", exact=False)).to_be_visible()
    expect(panel.get_by_text("Hora de caja desconocida en los meses revisados: 1.", exact=False)).to_be_visible()
    expect(panel.get_by_text("Fecha de registro desconocida en este navegador: 1", exact=False)).to_be_visible()
    expect(panel.get_by_text("QA-OTRO-LOCAL", exact=False)).to_have_count(0)
    expect(panel.get_by_text("QA-OTRA-CUENTA", exact=False)).to_have_count(0)
    crossing = panel.get_by_role("list", name="Ventas sincronizadas con horarios en meses distintos")
    expect(crossing.get_by_role("link", name=receipt["id"])).to_have_attribute(
        "href", f"/pos#entity=1&outlet=1&venta={receipt['id']}")
    assert crossing.locator("time").evaluate_all("times => times.map(time => time.dateTime)") == [registered_at, claim]
    assert panel.evaluate("el => el.scrollWidth <= el.clientWidth")
    assert page.evaluate("window.qaWriteTransactions") == 0
    assert not api and snapshot() == before

    # Eviction can occur after the UI checked the lease. The inspector must
    # disallow the IDB version-change transaction as well as ordinary writes.
    assert page.evaluate("!!sessionStorage.getItem('gomar-pos-active-lease')")
    page.evaluate("""() => new Promise((resolve, reject) => {
      const removed = indexedDB.deleteDatabase('gomar-pos-v1');
      removed.onsuccess = resolve;
      removed.onerror = () => reject(removed.error);
      removed.onblocked = () => reject(new Error('Fixture IDB is still open'));
    })""")
    names = "() => indexedDB.databases().then(dbs => dbs.map(db => db.name))"
    assert "gomar-pos-v1" not in page.evaluate(names)
    inspect.click()
    expect(panel.get_by_role("alert")).to_contain_text("No se pudieron leer las ventas")
    expect(crossing).to_have_count(0)
    assert "gomar-pos-v1" not in page.evaluate(names)
    assert page.evaluate("!!sessionStorage.getItem('gomar-pos-active-lease')")
    assert page.evaluate("window.qaWriteTransactions") == 0
    assert not api and snapshot() == before

    # Version 1 is IndexedDB's minimum. A broken v1 database without either
    # store must likewise remain unchanged rather than produce an empty result.
    page.evaluate("""() => new Promise((resolve, reject) => {
      const broken = indexedDB.open('gomar-pos-v1', 1);
      broken.onsuccess = () => { broken.result.close(); resolve(); };
      broken.onerror = () => reject(broken.error);
    })""")
    inspect.click()
    expect(panel.get_by_role("alert")).to_contain_text("No se pudieron leer las ventas")
    assert page.evaluate("""() => new Promise((resolve, reject) => {
      const inspected = indexedDB.open('gomar-pos-v1');
      inspected.onsuccess = () => {
        const data = [inspected.result.version, Array.from(inspected.result.objectStoreNames)];
        inspected.result.close();
        resolve(data);
      };
      inspected.onerror = () => reject(inspected.error);
      inspected.onupgradeneeded = () => { inspected.transaction.abort(); };
    })""") == [1, []]
    assert page.evaluate("window.qaWriteTransactions") == 0
    assert not api and snapshot() == before
    # Remove the broken fixture before exercising the ordinary lock path.
    page.evaluate("() => new Promise(resolve => { indexedDB.deleteDatabase('gomar-pos-v1').onsuccess = resolve })")
    page.get_by_role("button", name="Bloquear caja").click()
    expect(panel).to_have_count(0)
    assert snapshot() == before
