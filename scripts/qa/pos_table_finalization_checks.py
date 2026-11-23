"""Real desktop and mobile confirmation with scoped MCP readiness."""
from playwright.sync_api import expect
from pos_prebill_checks import use_fixture_connection
from pos_table_checks import scope, tool


def check_finalization(browser, login, origin, psql, issue, connection):
    table = 9
    query = {**scope, "table_number": table}
    handoff = f"/pos#entity=1&outlet=1&table={table}"

    def register(mobile=False, dest="/pos"):
        page = login(browser, "qa-pos-limited", mobile=mobile, allow_pos_worker=True)
        use_fixture_connection(page)
        page.goto(origin + dest)
        page.get_by_role("button", name="Habilitar caja en esta pestaña").click()
        panel = page.get_by_role("region", name="Cuentas guardadas de mesa")
        return page, panel

    def snapshot():
        return psql("""SELECT (SELECT count(*) FROM gomar.pos_sales)||'|'||
          (SELECT count(*) FROM gomar.pos_table_finalizations)||'|'||
          (SELECT count(*) FROM gomar.stock_movements)||'|'||
          (SELECT count(*) FROM gomar.ap_payments)||'|'||
          (SELECT count(*) FROM gomar.reservation_requests);""")

    initial = snapshot().split("|")
    keeper = login(browser, "qa-admin")
    reader = login(browser, "qa-reader")
    owner_key, _ = issue(keeper, "finalization-owner")
    reader_key, _ = issue(reader, "finalization-reader")
    desktop, panel = register()
    desktop.get_by_role("button", name="Agregar QA Pan tostado").click()
    panel.get_by_label("Número de mesa").fill(str(table))
    panel.get_by_role("button", name="Guardar cuenta sin cobro").click()
    tender = panel.get_by_role("region", name="Registro de medios de pago")
    expect(tender.get_by_role("status")).to_contain_text("7,50")
    with connection(owner_key) as mcp:
        assert mcp(tool, query)["state"] == "untendered"
        for value, method in (("2,50", "efectivo"), ("5,00", "debito")):
            tender.get_by_label("Importe ingresado").fill(value)
            tender.get_by_label("Medio usado").select_option(method)
            tender.get_by_role("button", name="Anotar importe").click()
            expect(tender.get_by_role("status")).to_contain_text(value if method == "efectivo" else "Registrado: $ 7,50")
        expect(tender.get_by_role("button", name="Revisar y registrar venta")).to_be_disabled()
        status = mcp(tool, query)
        assert status == {"table_number": table, "total_minor": 750, "state": "ready_for_review", "handoff_url": handoff}
        mcp(tool, {**query, "outlet_id": 2}, denied=True)
        mcp(tool, {**query, "entity_id": 2}, denied=True)
    with connection(reader_key) as mcp:
        mcp(tool, query, denied=True)
    assert snapshot().split("|") == initial

    mobile, panel = register(True, handoff)
    expect(panel.get_by_label("Número de mesa")).to_have_value(str(table))
    panel.get_by_role("button", name="Consultar mesa").click()
    tender = panel.get_by_role("region", name="Registro de medios de pago")
    tender.get_by_role("button", name="Revisar y registrar venta").click()
    tender.get_by_role("button", name="Volver sin registrar").click()
    assert snapshot().split("|") == initial
    tender.get_by_role("button", name="Revisar y registrar venta").click()
    tender.get_by_role("button", name="Confirmar venta").click()
    expect(panel.get_by_role("heading", name="Venta registrada: mesa 9")).to_be_visible()
    expect(panel.get_by_text("La mesa quedó libre. Esta acción no emite una factura.")).to_be_visible()
    after = snapshot().split("|")
    assert [int(after[i]) - int(initial[i]) for i in range(3)] == [1, 1, 1], (initial, after)
    assert after[3:] == initial[3:]
    with connection(owner_key) as mcp:
        mcp(tool, query, denied=True)
    mobile.get_by_role("button", name="Agregar QA Pan tostado").click()
    panel.get_by_role("button", name="Guardar cuenta sin cobro").click()
    expect(panel.get_by_role("heading", name="Cuenta guardada: mesa 9")).to_be_visible()
    with connection(owner_key) as mcp:
        assert mcp(tool, query)["state"] == "untendered"
    assert snapshot().split("|") == after
    for page in (desktop, mobile, keeper, reader):
        page.context.close()
    print("PASS: explicit table sale, Chrome desktop/mobile, scoped MCP readiness, exact stock write and table reuse")
