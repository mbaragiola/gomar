"""A synthetic staff reservation approval and scoped wiki reads, never public intake."""
from datetime import datetime, timedelta
from zoneinfo import ZoneInfo

from playwright.sync_api import expect

zone = ZoneInfo("America/Argentina/Buenos_Aires")
start = (datetime.now(zone) + timedelta(days=1)).replace(hour=21, minute=0, second=0, microsecond=0)
date = start.date().isoformat()
reference = "00000000000000000000000000000001"
foreign = "00000000000000000000000000000002"
wiki_body = "Ejemplo local sin datos de clientes."
confirmation_tool = "gomar_propose_reservation_confirmation"


def booking_seed(sql):
    sql(f"""
BEGIN;
SET search_path=gomar,public;
INSERT INTO reservation_venues VALUES
 (1,'qa-centro','QA Centro','qa-booking-hidden-policy',true),
 (2,'qa-norte','QA Norte','qa-booking-hidden-policy',true);
INSERT INTO reservation_tables VALUES (1,'Mesa 1',4),(2,'Mesa 2',4);
INSERT INTO reservation_slots VALUES
 (1,'{start.astimezone(ZoneInfo("UTC")).strftime("%Y-%m-%dT%H:%M:%SZ")}'),(2,'{start.astimezone(ZoneInfo("UTC")).strftime("%Y-%m-%dT%H:%M:%SZ")}');
INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,created_at)
 SELECT id,outlet,start_at,'qa-booking-hidden-synthetic',4,'pending','2026-11-01T00:00:00Z'
 FROM reservation_slots JOIN (VALUES
 (1,'{reference}'),(2,'{foreign}')) AS hints(outlet,id) ON outlet_id=outlet;
INSERT INTO knowledge_articles(id,outlet_id,title,body,search_text,version,created_at,updated_at) VALUES
 (1,1,'QA Apertura','{wiki_body}','qa apertura',1,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z'),
 (2,2,'QA Apertura Norte','qa-booking-hidden-wiki','qa apertura norte',1,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z');
INSERT INTO knowledge_article_versions(article_id,version,actor_id,recorded_at)
 SELECT article,1,id,'2026-11-01T00:00:00Z' FROM users CROSS JOIN (VALUES (1),(2)) AS rows(article)
 WHERE username='qa-admin';
COMMIT;
""")


def booking_tools(call, scope, allowed):
    article = {"id": 1, "title": "QA Apertura", "version": 1, "updated_at": "2026-11-01T00:00:00Z"}
    assert call("gomar_search_knowledge", {**scope, "query": "apertura"}) == {
        "articles": [article], "truncated": False,
    }
    assert call("gomar_read_knowledge_article", {**scope, "article_id": 1}) == {**article, "body": wiki_body}
    call("gomar_read_knowledge_article", {**scope, "article_id": 2}, denied=True)
    call("gomar_read_knowledge_article", {**scope, "article_id": 2, "outlet_id": 2}, denied=True)
    call("gomar_search_knowledge", {**scope, "entity_id": 2}, denied=True)
    args = {**scope, "date": date}
    if not allowed:
        call("gomar_list_reservation_requests", args, denied=True)
        return

    call("gomar_list_reservation_requests", {**args, "outlet_id": 2}, denied=True)
    call("gomar_read_reservation_request", {**args, "reservation_id": foreign}, denied=True)


def assert_queue(call, scope, status):
    row = {"id": reference, "status": status, "party_size": 4, "start_at": start.isoformat()}
    args = {**scope, "date": date}
    assert call("gomar_list_reservation_requests", args) == {
        "date": date, "timezone": zone.key, "requests": [row],
    }
    assert call("gomar_read_reservation_request", {**args, "reservation_id": reference}) == row


def snapshot(sql):
    return sql("""
SELECT
 (SELECT status FROM gomar.reservation_requests WHERE outlet_id=1),
 (SELECT coalesce(table_label,'') FROM gomar.reservation_requests WHERE outlet_id=1),
 (SELECT status FROM gomar.reservation_requests WHERE outlet_id=2),
 (SELECT count(*) FROM gomar.reservation_requests),
 (SELECT count(*) FROM gomar.knowledge_article_versions);
""")


def booking_checks(call, owner, peer, origin, scope, sql, review_panel, show):
    booking_tools(call, scope, allowed=True)
    assert_queue(call, scope, "pending")
    assert snapshot(sql) == "pending||pending|2|2"
    request = {**scope, "request_id": "00000000-0000-4000-8000-000000000007",
               "confirmation": {"reservation_reference": reference}}
    call(confirmation_tool, {**request, "outlet_id": 2}, denied=True)
    call(confirmation_tool, {**request, "confirmation": {"reservation_reference": foreign}}, denied=True)
    receipt = call(confirmation_tool, request)
    assert set(receipt) == {"id", "status", "expires_at"} and receipt["status"] == "pending"
    assert_queue(call, scope, "pending")
    assert snapshot(sql) == "pending||pending|2|2"
    path = f"/api/v1/entities/1/outlets/1/reservation-confirm-proposals/{receipt['id']}"
    status, rejection = peer.evaluate("""async path => {
      const response = await fetch(path);
      return [response.status, await response.text()];
    }""", path)
    assert status == 404, f"nonissuer proposal preview returned HTTP {status}"
    assert reference not in rejection and "qa-booking-hidden" not in rejection
    forbidden = []

    def staff_only(route):
        url = route.request.url
        if not url.startswith(origin + "/"):
            route.abort()
        elif "/api/v1/public/" in url or "/notifications" in url:
            forbidden.append(route.request.method)
            route.abort()
        else:
            route.continue_()

    owner.route("**/*", staff_only)

    panel = owner.get_by_role("region", name="Sesiones de propuestas")
    if panel.count() == 0:
        panel = review_panel(owner)
    preview = show(panel, receipt["id"], " de reserva")
    expect(preview.get_by_text(reference, exact=True)).to_be_visible()
    expect(preview.get_by_text("Mesa 1", exact=True)).to_be_visible()
    expect(preview.get_by_text("Solo se confirma esta reserva. No se envían mensajes.", exact=True)).to_be_visible()
    expect(preview).not_to_contain_text("qa-booking-hidden")
    preview.get_by_role("button", name="Continuar con la reserva").click()
    assert_queue(call, scope, "pending")
    preview.get_by_role("button", name="Confirmar reserva", exact=True).click()
    expect(panel.get_by_role("status")).to_contain_text("Reserva confirmada.")
    assert_queue(call, scope, "confirmed")
    assert snapshot(sql) == "confirmed|Mesa 1|pending|2|2"
    assert sql("SELECT count(*) FROM gomar.mcp_reservation_confirm_proposals WHERE status='approved'") == "1"
    assert sql("SELECT count(*) FROM gomar.mcp_reservation_confirm_proposals") == "1"
    assert not forbidden, "staff approval attempted public intake or notification"
