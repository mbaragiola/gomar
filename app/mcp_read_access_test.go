package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPostgresOwnMCPReadAccessRecoversOnlyCurrentPinnedHandles(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic fixture access password"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'fixture-owner',$1,TRUE,FALSE,FALSE),(2,'fixture-other',$1,TRUE,FALSE,FALSE)`, encodePassword(password))
	exec(`
		INSERT INTO entities(id,name,cuit,active) VALUES(1,'Business','00000000001',TRUE),(2,'Foreign','00000000002',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES (1,1,'Target',TRUE),(2,1,'Granted',TRUE),(3,1,'Ungranted',TRUE),(4,2,'Foreign',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES (1,1,1,'purchasing',TRUE),(2,2,1,'read_only',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1);
	`)
	now := time.Date(2030, 11, 4, 13, 0, 0, 0, time.UTC)
	router := (&app{db: db, host: "example.com", enableMCP: true, mcpClock: func() time.Time { return now }}).routes()
	disabled := (&app{db: db, host: "example.com"}).routes()
	owner := postgresTestCookies(t, router, "fixture-owner", password)
	freshOwner := postgresTestCookies(t, router, "fixture-owner", password)
	other := postgresTestCookies(t, router, "fixture-other", password)

	invoke := func(h http.Handler, method, path, body string, cookies []*http.Cookie, csrf bool, bearer string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
			if csrf && cookie.Name == "csrftoken" {
				req.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s status=%d want=%d", method, path, res.Code, want)
		}
		return res
	}
	type credential struct {
		ID    int64
		Token string
	}
	issue := func(cookies []*http.Cookie, body string) credential {
		t.Helper()
		var c credential
		r := invoke(router, "POST", "/api/v1/auth/mcp-token", body, cookies, true, "", 201)
		if err := json.Unmarshal(r.Body.Bytes(), &c); err != nil || c.ID <= 0 || len(c.Token) != 43 {
			t.Fatal("synthetic issue shape")
		}
		return c
	}
	const scope = `{"entity_id":1,"outlet_id":1}`
	const route = "/api/v1/entities/1/outlets/1/mcp-read-access"
	exp := issue(owner, scope)
	now = now.Add(15 * time.Minute)
	broad := issue(owner, `{"entity_id":1}`)
	otherOutlet := issue(owner, `{"entity_id":1,"outlet_id":2}`)
	otherActor := issue(other, scope)
	mismatch := issue(owner, scope)
	exec("UPDATE mcp_read_tokens SET password_digest=decode(repeat('01',32),'hex') WHERE id=$1", mismatch.ID)
	var want []string
	var latest credential
	for range 21 {
		latest = issue(owner, scope)
		want = append([]string{strconv.FormatInt(latest.ID, 10)}, want...)
	}

	read := func(path string, cookies []*http.Cookie, bearer string, want int) string {
		t.Helper()
		r := invoke(router, "GET", path, "", cookies, false, bearer, want)
		if r.Header().Get("Cache-Control") != "private, no-store" || r.Header().Get("Vary") != "Cookie" {
			t.Fatal("missing private cache controls")
		}
		out := strings.TrimSpace(r.Body.String())
		for _, secret := range []string{latest.Token, broad.Token, otherActor.Token, mismatch.Token, exp.Token, otherOutlet.Token, "password_digest", "token_hash", "fixture-owner", "outlet_id", "entity_id"} {
			if strings.Contains(out, secret) {
				t.Fatal("out-of-contract credential projection")
			}
		}
		return out
	}
	decode := func(text string) ([]string, string) {
		t.Helper()
		var page struct {
			Access   []map[string]string `json:"access"`
			BeforeID string              `json:"before_id"`
		}
		if err := json.Unmarshal([]byte(text), &page); err != nil || page.Access == nil {
			t.Fatal("bad metadata page shape")
		}
		ids := make([]string, 0, len(page.Access))
		for _, row := range page.Access {
			if len(row) != 3 || row["id"] == "" || row["issued_at"] != now.Format(time.RFC3339) ||
				row["expires_at"] != now.Add(mcpTokenLifetime).Format(time.RFC3339) {
				t.Fatal("bad metadata row shape")
			}
			ids = append(ids, row["id"])
		}
		return ids, page.BeforeID
	}
	ids, next := decode(read(route, freshOwner, "", 200))
	if !reflect.DeepEqual(ids, want[:20]) || next != want[19] {
		t.Fatal("first page did not match own unexpired scoped keys")
	}
	ids, next = decode(read(route+"?before_id="+next, freshOwner, "", 200))
	if !reflect.DeepEqual(ids, want[20:]) || next != "" {
		t.Fatal("later page did not match own unexpired scoped keys")
	}
	ids, _ = decode(read(route, other, "", 200))
	if !reflect.DeepEqual(ids, []string{strconv.FormatInt(otherActor.ID, 10)}) {
		t.Fatal("another actor could not safely recover only its own row")
	}
	ids, _ = decode(read("/api/v1/entities/1/outlets/2/mcp-read-access", owner, "", 200))
	if !reflect.DeepEqual(ids, []string{strconv.FormatInt(otherOutlet.ID, 10)}) {
		t.Fatal("another granted branch was mixed into the target")
	}
	for _, suffix := range []string{"?", "?before_id=0", "?before_id=01", "?before_id=1&before_id=2", "?other=1"} {
		read(route+suffix, owner, "", 422)
	}
	read(route, nil, "", 401)
	read(route, nil, latest.Token, 401)
	read(route, owner, latest.Token, 422)
	read("/api/v1/entities/1/outlets/3/mcp-read-access", owner, "", 404)
	read("/api/v1/entities/2/outlets/4/mcp-read-access", owner, "", 404)
	invoke(disabled, "GET", route, "", owner, false, "", 404)

	const ordinary = "/api/v1/mcp/me"
	invoke(router, "GET", ordinary, "", nil, false, latest.Token, 200)
	revoke := "/api/v1/auth/mcp-token/" + strconv.FormatInt(latest.ID, 10)
	invoke(router, "DELETE", revoke, "", other, true, "", 204)
	invoke(router, "GET", ordinary, "", nil, false, latest.Token, 200)
	invoke(router, "DELETE", revoke, "", freshOwner, false, "", 403)
	invoke(router, "GET", ordinary, "", nil, false, latest.Token, 200)
	invoke(router, "DELETE", revoke, "", freshOwner, true, "", 204)
	invoke(router, "GET", ordinary, "", nil, false, latest.Token, 401)
	ids, _ = decode(read(route, freshOwner, "", 200))
	if !reflect.DeepEqual(ids, want[1:]) {
		t.Fatal("revoked credential remained visible or other handles were deleted")
	}
	exec("UPDATE memberships SET role='unassigned' WHERE id=1")
	read(route, owner, "", 403)
	exec("UPDATE memberships SET role='purchasing' WHERE id=1")
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1")
	read(route, owner, "", 404)
	exec("INSERT INTO outlet_grants VALUES (1,1)")
	exec("UPDATE outlets SET active=FALSE WHERE id=1")
	read(route, owner, "", 404)
}
