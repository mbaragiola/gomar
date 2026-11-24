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
    desktop, origin_panel = register()
    attempts = []
    ordinary_sales = []
    save_url = origin + "/api/v1/entities/1/outlets/1/pos/table-checks"

    def lose_first_reply(route):
        # Commit the real local API write, then simulate a lost response to A.
        attempts.append(route.request.post_data_json)
        response = route.fetch(max_redirects=0)
        if len(attempts) == 1:
            assert response.status == 201
            route.abort()
        else:
            assert response.status == 200
            route.fulfill(response=response)

    desktop.route(save_url, lose_first_reply)
    desktop.on("request", lambda r: ordinary_sales.append(r.url) if r.method == "POST" and r.url.endswith("/pos/sales") else None)
    desktop.get_by_role("button", name="Agregar QA Pan tostado").click()
    desktop.get_by_label("Medio de pago").select_option("efectivo")
    expect(desktop.get_by_role("button", name="Registrar venta", exact=True)).to_be_enabled()
    origin_panel.get_by_label("Número de mesa").fill(str(table))
    origin_panel.get_by_role("button", name="Guardar cuenta sin cobro").click()
    expect(desktop.get_by_role("alert")).to_contain_text("No se pudo verificar la cuenta de la mesa 9.")
    expect(desktop.get_by_role("button", name="Registrar venta", exact=True)).to_be_disabled()
    expect(desktop.get_by_role("button", name="Agregar QA Pan tostado")).to_be_disabled()
    expect(origin_panel.get_by_label("Número de mesa")).to_be_disabled()
    assert not ordinary_sales
    assert snapshot().split("|") == initial

    with connection(owner_key) as mcp:
        first = mcp(tool, query)
        assert first == {"table_number": table, "total_minor": 750, "state": "untendered", "handoff_url": handoff}
    mobile, panel = register(True, first["handoff_url"])
    expect(panel.get_by_label("Número de mesa")).to_have_value(str(table))
    panel.get_by_role("button", name="Consultar mesa").click()
    tender = panel.get_by_role("region", name="Registro de medios de pago")
    expect(tender.get_by_role("status")).to_contain_text("7,50")
    with connection(owner_key) as mcp:
        for value, method in (("2,50", "efectivo"), ("5,00", "debito")):
            tender.get_by_label("Importe ingresado").fill(value)
            tender.get_by_label("Medio usado").select_option(method)
            tender.get_by_role("button", name="Anotar importe").click()
            expect(tender.get_by_role("status")).to_contain_text(value if method == "efectivo" else "Registrado: $ 7,50")
        expect(tender.get_by_role("button", name="Revisar y registrar venta")).to_be_enabled()
        status = mcp(tool, query)
        assert status == {"table_number": table, "total_minor": 750, "state": "ready_for_review", "handoff_url": handoff}
        mcp(tool, {**query, "outlet_id": 2}, denied=True)
        mcp(tool, {**query, "entity_id": 2}, denied=True)
    with connection(reader_key) as mcp:
        mcp(tool, query, denied=True)
    assert snapshot().split("|") == initial

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

    # Reload and rearm A after B finalized: the scoped hold survives both.
    desktop.reload()
    expect(origin_panel.get_by_label("Número de mesa")).to_have_value(str(table))
    expect(desktop.get_by_text("QA Pan tostado × 1", exact=True)).to_be_visible()
    expect(desktop.get_by_role("button", name="Registrar venta", exact=True)).to_be_disabled()
    desktop.get_by_role("button", name="Bloquear caja").click()
    expect(origin_panel).to_have_count(0)
    desktop.get_by_role("button", name="Habilitar caja en esta pestaña").click()
    expect(origin_panel.get_by_label("Número de mesa")).to_have_value(str(table))
    expect(desktop.get_by_text("QA Pan tostado × 1", exact=True)).to_be_visible()
    expect(desktop.get_by_role("button", name="Registrar venta", exact=True)).to_be_disabled()
    origin_panel.get_by_role("button", name="Reintentar guardar la misma cuenta").click()
    expect(origin_panel.get_by_role("heading", name="Venta registrada: mesa 9")).to_be_visible()
    expect(desktop.get_by_text("Agregá productos para empezar.")).to_be_visible()
    expect(desktop.get_by_role("button", name="Registrar venta", exact=True)).to_be_disabled()
    assert len(attempts) == 2 and attempts[0] == attempts[1], attempts
    assert not ordinary_sales and snapshot().split("|") == after

    mobile.get_by_role("button", name="Agregar QA Pan tostado").click()
    panel.get_by_role("button", name="Guardar cuenta sin cobro").click()
    expect(panel.get_by_role("heading", name="Cuenta guardada: mesa 9")).to_be_visible()
    with connection(owner_key) as mcp:
        assert mcp(tool, query)["state"] == "untendered"
    assert snapshot().split("|") == after
    for page in (desktop, mobile, keeper, reader):
        page.context.close()
    print("PASS: two Chrome registers, lost save/reload/rearm/exact replay, no ordinary duplicate, scoped MCP handoff, one synthetic table sale and stock write")
