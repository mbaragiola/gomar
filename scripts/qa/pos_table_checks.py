"""Test persisted, untendered POS table checks through Chrome and packaged MCP."""
from playwright.sync_api import expect

from pos_prebill_checks import use_fixture_connection

tool = "gomar_read_pos_table_check"
scope = {"entity_id": 1, "outlet_id": 1}
target = {**scope, "table_number": 7}
handoff = "/pos#entity=1&outlet=1&table=7"


def short_snapshot(psql):
    return psql("""SELECT (SELECT count(*) FROM gomar.pos_table_checks),
   (SELECT count(*) FROM gomar.pos_table_check_items),
   (SELECT count(*) FROM gomar.pos_table_check_slots),
   (SELECT count(*) FROM gomar.pos_table_check_retirements),
   (SELECT count(*) FROM gomar.pos_sales),
   (SELECT count(*) FROM gomar.stock_movements),
   (SELECT count(*) FROM gomar.ap_payments),
   (SELECT count(*) FROM gomar.reservation_requests);""")


def check_saved_table(browser, login, origin, psql, issue, connection):
    before = short_snapshot(psql).split("|")
    assert before[:4] == ["0", "0", "0", "0"], before
    unchanged = "|".join(["1", "1", "1", "0", *before[4:]])

    def open_pos(login_name, *, mobile=False, dest="/pos", branch=None):
        page = login(browser, login_name, mobile=mobile, allow_pos_worker=True)
        use_fixture_connection(page)
        page.goto(origin + dest)
        if branch:
            page.get_by_role("combobox", name="Local", exact=True).select_option(branch)
        return page

    keeper = login(browser, "qa-admin")
    reader = login(browser, "qa-reader")
    owner_key, _ = issue(keeper, "table-owner")
    reader_key, _ = issue(reader, "table-reader")
    desktop = open_pos("qa-pos-limited")
    desktop.get_by_role("button", name="Habilitar caja en esta pestaña").click()
    panel = desktop.get_by_role("region", name="Cuentas guardadas de mesa")
    expect(panel.get_by_role("button", name="Guardar cuenta sin cobro")).to_be_disabled()
    desktop.get_by_role("button", name="Agregar QA Pan de campo").click()
    desktop.get_by_label("Medio de pago").select_option("efectivo")
    expect(desktop.get_by_role("button", name="Registrar venta", exact=True)).to_be_enabled()
    panel.get_by_label("Número de mesa").fill("7")
    panel.get_by_role("button", name="Guardar cuenta sin cobro").click()
    expect(panel.get_by_role("heading", name="Cuenta guardada: mesa 7")).to_be_visible()
    expect(panel.get_by_text("1 × QA Pan de campo", exact=False)).to_be_visible()
    expect(desktop.get_by_text("Agregá productos para empezar.")).to_be_visible()
    expect(desktop.get_by_label("Medio de pago")).to_have_value("")
    expect(desktop.get_by_role("button", name="Registrar venta", exact=True)).to_be_disabled()
    expect(panel.get_by_role("button", name="Guardar cuenta sin cobro")).to_be_disabled()
    assert short_snapshot(psql) == unchanged
    original_panel = panel

    with connection(owner_key) as call:
        compatible = call(tool, target)
        assert compatible == {"table_number": 7, "total_minor": 750,
                              "state": "untendered", "handoff_url": handoff}
        call(tool, {**target, "outlet_id": 2}, denied=True)
        call(tool, {**target, "entity_id": 2}, denied=True)
        call(tool, {**target, "table_number": 8}, denied=True)
    with connection(reader_key) as call:
        call(tool, target, denied=True)

    linked = open_pos("qa-pos-limited", mobile=True, dest=compatible["handoff_url"])
    accesses = []
    linked.on("request", lambda r: accesses.append(r.method) if "/pos/table-checks" in r.url and not r.url.endswith("/tender") else None)
    linked.get_by_role("button", name="Habilitar caja en esta pestaña").click()
    panel = linked.get_by_role("region", name="Cuentas guardadas de mesa")
    expect(panel.get_by_label("Número de mesa")).to_have_value("7")
    assert not accesses
    panel.get_by_role("button", name="Consultar mesa").click()
    expect(panel.get_by_role("heading", name="Cuenta guardada: mesa 7")).to_be_visible()
    expect(panel.get_by_text("1 × QA Pan de campo", exact=False)).to_be_visible()
    assert accesses == ["GET"], accesses

    # A definitive rejection cannot consume this separate local cart.
    linked.get_by_role("button", name="Agregar QA Pan de campo").click()
    linked.get_by_label("Medio de pago").select_option("efectivo")
    panel.get_by_role("button", name="Guardar cuenta sin cobro").click()
    expect(linked.get_by_role("alert")).to_contain_text("Esta mesa ya tiene una cuenta guardada.")
    expect(linked.get_by_text("QA Pan de campo × 1", exact=True)).to_be_visible()
    expect(linked.get_by_label("Medio de pago")).to_have_value("efectivo")
    expect(linked.get_by_role("button", name="Registrar venta", exact=True)).to_be_enabled()
    assert short_snapshot(psql) == unchanged
    linked.get_by_role("button", name="Quitar QA Pan de campo").click()
    panel.get_by_role("button", name="Consultar mesa").click()
    expect(panel.get_by_role("heading", name="Cuenta guardada: mesa 7")).to_be_visible()
    assert accesses == ["GET", "POST", "GET"], accesses
    panel.get_by_role("button", name="Retirar cuenta sin cobro").click()
    expect(panel.get_by_role("button", name="Confirmar retiro")).to_be_disabled()
    panel.get_by_label("Motivo del retiro").select_option("cancelled")
    panel.get_by_role("button", name="Volver").click()
    assert accesses == ["GET", "POST", "GET"], accesses
    panel.get_by_role("button", name="Retirar cuenta sin cobro").click()
    panel.get_by_label("Motivo del retiro").select_option("cancelled")
    panel.get_by_role("button", name="Confirmar retiro").click()
    expect(panel.get_by_role("heading", name="Cuenta retirada: mesa 7")).to_be_visible()
    assert accesses == ["GET", "POST", "GET", "POST"], accesses
    assert short_snapshot(psql) == "|".join(["1", "1", "0", "1", *before[4:]])
    with connection(owner_key) as call:
        call(tool, target, denied=True)
    linked.context.close()

    expect(original_panel.get_by_role("button", name="Guardar cuenta sin cobro")).to_be_disabled()
    assert short_snapshot(psql) == "|".join(["1", "1", "0", "1", *before[4:]])
    desktop.get_by_role("button", name="Agregar QA Pan de campo").click()
    original_panel.get_by_role("button", name="Guardar cuenta sin cobro").click()
    expect(original_panel.get_by_role("heading", name="Cuenta guardada: mesa 7")).to_be_visible()
    assert short_snapshot(psql) == "|".join(["2", "2", "1", "1", *before[4:]])
    with connection(owner_key) as call:
        assert call(tool, target) == compatible
    # The same issued MCP key stops reading this handoff when the user's
    # current selling role or exact branch grant is removed.
    with connection(owner_key) as call:
        psql("UPDATE gomar.memberships SET role='read_only' WHERE id=1")
        try:
            call(tool, target, denied=True)
        finally:
            psql("UPDATE gomar.memberships SET role='administration' WHERE id=1")
        psql("DELETE FROM gomar.outlet_grants WHERE membership_id=1 AND outlet_id=1")
        try:
            call(tool, target, denied=True)
        finally:
            psql("INSERT INTO gomar.outlet_grants VALUES(1,1)")
        assert call(tool, target) == compatible
    desktop.context.close()

    wrong = open_pos("qa-pos-limited", dest="/pos#entity=1&outlet=2&table=7")
    wrong.get_by_role("button", name="Habilitar caja en esta pestaña").click()
    panel = wrong.get_by_role("region", name="Cuentas guardadas de mesa")
    expect(panel.get_by_label("Número de mesa")).to_have_value("")
    wrong_endpoint = origin + "/api/v1/entities/1/outlets/2/pos/table-checks/7"
    no_access = wrong.request.get(wrong_endpoint)
    assert no_access.status == 404 and "QA Pan de campo" not in no_access.text()
    wrong.context.close()

    denied = open_pos("qa-reader", dest=handoff)
    expect(denied.get_by_role("button", name="Habilitar caja en esta pestaña")).to_be_disabled()
    expect(denied.get_by_role("region", name="Cuentas guardadas de mesa")).to_have_count(0)
    no_access = denied.request.get(origin + "/api/v1/entities/1/outlets/1/pos/table-checks/7")
    assert no_access.status == 403 and "QA Pan de campo" not in no_access.text()
    denied.context.close()
    outsider = browser.new_page()
    use_fixture_connection(outsider)
    outsider.goto(origin + handoff)
    expect(outsider.get_by_role("link", name="Ingresá al espacio de trabajo")).to_be_visible()
    expect(outsider.get_by_role("region", name="Cuentas guardadas de mesa")).to_have_count(0)
    outsider.close()
    keeper.context.close()
    reader.context.close()
    assert short_snapshot(psql) == "|".join(["2", "2", "1", "1", *before[4:]])
