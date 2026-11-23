"""Native POS handoff: Chrome print preview without printers or fiscal calls."""
import subprocess

from playwright.sync_api import expect

legend = "DOCUMENTO NO VÁLIDO COMO FACTURA"


def pdf_text(page, width):
    pdf = page.pdf(width=width, height="210mm")
    return subprocess.run(["pdftotext", "-layout", "-", "-"], input=pdf,
                          capture_output=True, check=True).stdout.decode()


def use_fixture_connection(page):
    # Chrome's OS connectivity hint can be false when the test only has a
    # loopback interface. All actual requests still use the live local app.
    page.add_init_script("Object.defineProperty(Navigator.prototype, 'onLine', {get: () => true})")


def sale_count(page):
    return page.evaluate("""() => new Promise((resolve, reject) => {
      const open = indexedDB.open('gomar-pos-v1');
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const db = open.result;
        const count = db.transaction('sales').objectStore('sales').count();
        count.onerror = () => { db.close(); reject(count.error) };
        count.onsuccess = () => { db.close(); resolve(count.result) };
      };
    })""")


def check_prebill(browser, login, origin):
    for mobile in (False, True):
        page = login(browser, "qa-pos-limited", mobile=mobile, allow_pos_worker=True)
        use_fixture_connection(page)
        page.add_init_script("window.print = () => { window.qaPrintCalls = (window.qaPrintCalls || 0) + 1 }")
        sale_writes = []
        page.on("request", lambda request: sale_writes.append(request.url)
                if request.method == "POST" and "/pos/sales" in request.url else None)
        page.goto(origin + "/pos")
        shift = page.get_by_role("button", name="Habilitar caja en esta pestaña")
        expect(shift).to_be_enabled(timeout=10000)
        expect(page.get_by_role("combobox", name="Local", exact=True).locator("option")).to_have_text(["QA Centro"])
        foreign = page.request.get(origin + "/api/v1/entities/1/outlets/2/pos/catalog")
        assert foreign.status in (403, 404) and "QA otro local" not in foreign.text()
        shift.click()
        view = page.get_by_role("button", name="Ver cuenta de mesa")
        expect(view).to_be_disabled()
        add = page.get_by_role("button", name="Agregar QA Pan de campo")
        add.click()
        add.click()
        page.get_by_role("button", name="Agregar QA Galleta").click()
        expect(view).to_be_enabled()
        view.click()
        preview = page.get_by_role("region", name="Cuenta de mesa")
        expect(preview.get_by_text(legend, exact=True)).to_be_visible()
        expect(preview.get_by_text("No acredita pago.", exact=False)).to_be_visible()
        expect(page.get_by_label("Medio de pago")).to_have_value("")
        expect(page.get_by_role("button", name="Registrar venta")).to_be_disabled()
        assert page.evaluate("window.qaPrintCalls || 0") == 0
        preview.get_by_role("button", name="Imprimir cuenta de mesa").click()
        assert page.evaluate("window.qaPrintCalls") == 1
        rendered = pdf_text(page, "58mm" if mobile else "80mm")
        compact = " ".join(rendered.split())
        assert legend in compact and "No acredita pago." in compact, repr(rendered)
        assert "2 × QA Pan de campo" in compact and "1 × QA Galleta" in compact, repr(rendered)
        assert "Total de la cuenta:" in compact and "20,50" in compact, repr(rendered)
        assert "Medio de pago" not in compact and "Ventas de esta caja" not in compact
        assert "Imprimir cuenta de mesa" not in compact and "QA otro local" not in compact
        assert not sale_writes and sale_count(page) == 0
        expect(page.get_by_text("No hay ventas guardadas para este local.")).to_be_visible()

        # An item change forces a fresh preview and a different printed total.
        add.click()
        expect(preview).to_have_count(0)
        assert "Cuenta de mesa" not in pdf_text(page, "80mm")
        view.click()
        expect(preview.get_by_text("Total de la cuenta:")).to_contain_text("28,00")
        # Lost same-tab lease before timer dispatch: do not print stale content,
        # including with Chrome's own print shortcut.
        page.evaluate("sessionStorage.removeItem('gomar-pos-active-lease')")
        preview.get_by_role("button", name="Imprimir cuenta de mesa").click()
        assert page.evaluate("window.qaPrintCalls") == 1
        assert "QA Pan de campo" not in pdf_text(page, "80mm")
        assert not sale_writes and sale_count(page) == 0
        page.context.close()

    reader = login(browser, "qa-reader", allow_pos_worker=True)
    use_fixture_connection(reader)
    reader.goto(origin + "/pos")
    expect(reader.get_by_text("Tu rol no permite registrar ventas en este local.")).to_be_visible()
    expect(reader.get_by_role("button", name="Habilitar caja en esta pestaña")).to_be_disabled()
    expect(reader.get_by_role("region", name="Cuenta de mesa")).to_have_count(0)
    reader.context.close()

    outsider = browser.new_page()
    use_fixture_connection(outsider)
    outsider.goto(origin + "/pos")
    expect(outsider.get_by_role("link", name="Ingresá al espacio de trabajo")).to_be_visible()
    expect(outsider.get_by_role("region", name="Cuenta de mesa")).to_have_count(0)
    outsider.close()
