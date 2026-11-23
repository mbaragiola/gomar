"""Real Chrome for clerk notes; packaged MCP sees only scoped status and handoff."""
from playwright.sync_api import expect

from pos_prebill_checks import sale_count, use_fixture_connection
from pos_table_checks import scope, short_snapshot, tool


def check_manual_tender(browser, login, origin, psql, issue, connection, with_finalization=False):
    table_number = 8
    query = {**scope, "table_number": table_number}
    handoff = f"/pos#entity=1&outlet=1&table={table_number}"
    endpoint = origin + f"/api/v1/entities/1/outlets/1/pos/table-checks/{table_number}/tender"
    before = short_snapshot(psql).split("|")
    keeper = login(browser, "qa-admin")
    reader = login(browser, "qa-reader")
    owner_key, _ = issue(keeper, "tender-owner")
    reader_key, _ = issue(reader, "tender-reader")
    assert reader.request.get(endpoint).status == 403

    def register(mobile=False, dest="/pos"):
        page = login(browser, "qa-pos-limited", mobile=mobile, allow_pos_worker=True)
        use_fixture_connection(page)
        page.goto(origin + dest)
        page.get_by_role("button", name="Habilitar caja en esta pestaña").click()
        return page, page.get_by_role("region", name="Cuentas guardadas de mesa")

    desktop, panel = register()
    desktop.get_by_role("button", name="Agregar QA Pan de campo").click()
    desktop.get_by_role("button", name="Agregar QA Pan de campo").click()
    panel.get_by_label("Número de mesa").fill(str(table_number))
    panel.get_by_role("button", name="Guardar cuenta sin cobro").click()
    tender = panel.get_by_role("region", name="Registro de medios de pago")
    expect(tender.get_by_role("status")).to_contain_text("15,00")
    assert desktop.request.get(endpoint.replace("/outlets/1/", "/outlets/2/")).status == 404

    def record(cents, method):
        tender.get_by_label("Importe ingresado").fill(cents)
        tender.get_by_label("Medio usado").select_option(method)
        tender.get_by_role("button", name="Anotar importe").click()

    with connection(owner_key) as mcp:
        assert mcp(tool, query) == {"table_number": table_number, "total_minor": 1500, "state": "untendered", "handoff_url": handoff}
        record("5,50", "efectivo")
        expect(tender.get_by_role("status")).to_contain_text("5,50")
        record("9,50", "debito")
        expect(tender.get_by_role("status")).to_contain_text("Registrado: $ 15,00")
        expect(tender.get_by_role("button", name="Anotar importe")).to_have_count(0)
        expect(panel.get_by_role("button", name="Retirar cuenta sin cobro")).to_have_count(0)
        status = mcp(tool, query)
        assert status == {"table_number": table_number, "total_minor": 1500, "state": "ready_for_review" if with_finalization else "manual_records", "handoff_url": handoff}
        mcp(tool, {**query, "outlet_id": 2}, denied=True)
        mcp(tool, {**query, "entity_id": 2}, denied=True)
    with connection(reader_key) as mcp:
        mcp(tool, query, denied=True)
    assert sale_count(desktop) == 0
    desktop.context.close()

    mobile, panel = register(mobile=True, dest=status["handoff_url"])
    expect(panel.get_by_label("Número de mesa")).to_have_value(str(table_number))
    panel.get_by_role("button", name="Consultar mesa").click()
    tender = panel.get_by_role("region", name="Registro de medios de pago")
    expect(tender.get_by_role("status")).to_contain_text("Registrado: $ 15,00")
    history = tender.get_by_role("list", name="Historial de registros de pago")
    history.get_by_role("listitem").filter(has_text="Efectivo").get_by_role("button", name="Corregir registro").click()
    expect(tender.get_by_text("Corregir esta anotación no devuelve dinero.", exact=False)).to_be_visible()
    tender.get_by_label("Motivo de la corrección").select_option("importe_incorrecto")
    tender.get_by_role("button", name="Confirmar corrección").click()
    expect(tender.get_by_text("Corrección: Importe incorrecto", exact=True)).to_be_visible()
    expect(tender.get_by_role("status")).to_contain_text("Registrado: $ 9,50")
    expect(history.get_by_role("listitem").filter(has_text="Efectivo")).to_contain_text("Corregido")
    after = short_snapshot(psql).split("|")
    assert [int(after[i]) - int(before[i]) for i in range(4)] == [1, 1, 1, 0]
    assert before[4:] == after[4:]
    assert psql("SELECT count(*) FROM gomar.pos_table_tender_events") == "3"
    assert sale_count(mobile) == 0
    mobile.context.close()
    keeper.context.close()
    reader.context.close()
