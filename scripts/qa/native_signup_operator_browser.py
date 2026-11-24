"""Protected recovery authors one exact plan; real Chrome owner sees only that display."""
import os
import sys

from playwright.sync_api import expect, sync_playwright


owner_origin, private_origin = sys.argv[1:]
operator_path = "/admin/alta/plan/"
case_ref = "private-request-operator-screen"
organization = "Marca  del operador"
local_a, local_b = "Centro <QA>", "Norte  del operador"


def context_for(browser, mobile=False):
    ctx = browser.new_context(ignore_https_errors=True, is_mobile=mobile, has_touch=mobile,
                              viewport={"width": 390 if mobile else 1280, "height": 820},
                              service_workers="block")
    ctx.route("**/*", lambda route: route.continue_() if route.request.url.startswith(
        (owner_origin + "/", private_origin + "/")) else route.abort())
    return ctx


def submit_operator(page, button):
    with page.expect_response(lambda res: res.url == private_origin + operator_path and res.request.method == "POST") as sent:
        page.get_by_role("button", name=button).click()
    response = sent.value
    headers = response.request.all_headers()
    assert headers.get("origin") == private_origin and response.status == 200
    assert response.headers["referrer-policy"] == "same-origin"


def wrong_origin_denied(ctx, csrf):
    # An independent test page on the other local authority posts a real
    # browser form. Navigation exposes the HTTP denial to the harness
    # without adding CORS access to the protected app.
    page = ctx.new_page()
    foreign_fixture = owner_origin + "/__signup-foreign-origin-fixture"
    try:
        page.route(foreign_fixture, lambda route: route.fulfill(
            status=200, content_type="text/html", headers={"Referrer-Policy": "strict-origin"},
            body="<!doctype html><html><head><title>Other origin fixture</title></head><body></body></html>"))
        assert page.goto(foreign_fixture).status == 200
        assert page.evaluate("location.origin") == owner_origin
        with page.expect_response(lambda res: res.url == private_origin + operator_path and res.request.method == "POST", timeout=5000) as sent:
            page.evaluate("""([dest, token, code]) => {
              const form = document.createElement('form');
              form.method = 'POST';
              form.action = dest;
              for (const [name, value] of Object.entries({action:'open', csrfmiddlewaretoken:token, case:code})) {
                const input = document.createElement('input');
                input.type = 'hidden';
                input.name = name;
                input.value = value;
                form.append(input);
              }
              document.body.append(form);
              setTimeout(() => form.submit(), 0);
            }""", [private_origin + operator_path, csrf, case_ref])
        response = sent.value
        headers = response.request.all_headers()
        assert headers.get("origin") == owner_origin
        assert "__Secure-gomar_admin=" in headers.get("cookie", "")
        assert response.status == 403
    finally:
        page.close()

def author(browser):
    with context_for(browser) as ctx:
        page = ctx.new_page()
        assert page.goto(private_origin + operator_path).status == 404
        assert page.goto(private_origin + "/admin/login/").status == 200
        page.get_by_label("Usuario", exact=True).fill("same-email@example.invalid")
        page.get_by_label("Contraseña", exact=True).fill("synthetic-recovery-password")
        page.get_by_role("button", name="Iniciar sesión").click()
        assert page.goto(private_origin + operator_path).status == 200
        expect(page.get_by_role("heading", name="Preparar el alta")).to_be_visible()
        csrf = page.locator("#signup-plan-form input[name='csrfmiddlewaretoken']").input_value()
        wrong_origin_denied(ctx, csrf)
        page.get_by_label("Referencia privada del caso").fill(case_ref)
        submit_operator(page, "Buscar caso")
        expect(page.get_by_label("Nombre visible de la organización")).to_be_visible()
        assert case_ref not in page.url and case_ref not in page.content()
        page.get_by_label("Nombre visible de la organización").fill(organization)
        page.get_by_label("Nombre visible del local outlet-a").fill(local_a)
        page.get_by_label("Nombre visible del local outlet-b").fill(local_b)
        submit_operator(page, "Revisar etiquetas")
        expect(page.get_by_role("heading", name="Revisá antes de guardar")).to_be_visible()
        assert page.locator(".signup-plan-label").all_text_contents() == [organization, local_a, local_b]
        expect(page.locator("#signup-plan-form input[name='branch_0']")).to_have_count(0)
        assert case_ref not in page.url and case_ref not in page.content()
        ticket = page.locator("#signup-plan-form input[name='ticket']").input_value()
        csrf = page.locator("#signup-plan-form input[name='csrfmiddlewaretoken']").input_value()
        submit_operator(page, "Guardar plan")
        expect(page.get_by_role("status")).to_contain_text("Plan guardado")
        expect(page.get_by_role("status")).to_contain_text("no registra un pago ni habilita el acceso")
        assert case_ref not in page.url and case_ref not in page.content()
        replay = page.request.post(private_origin + operator_path, headers={"Origin": private_origin},
                                   form={"action": "save", "ticket": ticket, "confirm": "yes", "csrfmiddlewaretoken": csrf})
        assert replay.status == 404 and case_ref not in replay.text()


def owner_review(browser, mobile):
    with context_for(browser, mobile) as ctx:
        page = ctx.new_page()
        assert page.goto(owner_origin + "/alta").status == 404
        assert page.request.get(owner_origin + "/__signup-fixture-sign-in/other").status == 204
        assert page.request.get(owner_origin + operator_path).status == 404
        assert page.goto(owner_origin + "/alta").status == 200
        page.get_by_role("button", name="Ver solicitud").click()
        expect(page.get_by_role("heading", name="Marca del operador")).to_be_visible()
        names = page.get_by_role("list", name="Locales incluidos").locator("li")
        expect(names).to_have_text(["Centro <QA>", "Norte del operador"])
        assert names.all_text_contents() == [local_a, local_b]
        assert page.get_by_role("heading", name="Marca del operador").text_content() == organization
        assert case_ref not in page.content() and "outlet-a" not in page.content()
        assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(executable_path=os.environ.get("QA_CHROME", "/opt/google/chrome/chrome"),
        args=["--no-sandbox", "--disable-background-networking", "--no-proxy-server",
              "--host-resolver-rules=EXCLUDE 127.0.0.1, MAP * ~NOTFOUND"])
    try:
        author(browser)
        owner_review(browser, False)
        owner_review(browser, True)
    finally:
        browser.close()
print("PASS: protected Chrome form requires exact same Origin; credentialed foreign Origin denied")
print("PASS: protected recovery operator authored exact native owner display in Chrome desktop/mobile")
