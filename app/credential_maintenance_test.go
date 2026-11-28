package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresCredentialMaintenance(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	const password = "synthetic credential maintenance password"
	hash := encodePassword(password)
	digest := sha256.Sum256([]byte(hash))
	exec(`INSERT INTO users(id,username,password,active,staff,superuser)
		VALUES(1,'fixture-key-owner',$1,TRUE,FALSE,FALSE),(2,'fixture-admin',$1,TRUE,TRUE,TRUE)`, hash)
	exec(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000000',TRUE),(2,'Other','00000000019',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active)
		VALUES(1,1,1,'administration',TRUE),(2,1,2,'administration',TRUE)`)
	a := &app{db: db, host: "example.com", enableMCP: true, mcpClock: func() time.Time { return now }}
	handler := a.routes()
	browser := postgresTestCookies(t, handler, "fixture-key-owner", password)
	call := func(method, path, body, bearer string, cookies []*http.Cookie, status int) []byte {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		for _, cookie := range cookies {
			req.AddCookie(cookie)
			if cookie.Name == "csrftoken" {
				req.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: status=%d, want=%d", method, path, rec.Code, status)
		}
		return rec.Body.Bytes()
	}
	const me = "/api/v1/auth/me"
	const askKey = "/api/v1/auth/mcp-token"
	const mcpMe = "/api/v1/mcp/me"
	readKey := func(entity int64) string {
		t.Helper()
		var key struct{ Token string }
		if err := json.Unmarshal(call("POST", askKey, fmt.Sprintf(`{"entity_id":%d}`, entity), "", browser, 201), &key); err != nil || key.Token == "" {
			t.Fatal("missing synthetic read key", err)
		}
		return key.Token
	}
	activeKey := readKey(1)
	expiredKeyRaw := []byte(strings.Repeat("b", 32))
	expiredKey := base64.RawURLEncoding.EncodeToString(expiredKeyRaw)
	expiredKeyHash := sha256.Sum256(expiredKeyRaw)
	expiredCookie := strings.Repeat("c", 32)
	expiredCookieHash := sha256.Sum256([]byte(expiredCookie))
	recoveryCookie := strings.Repeat("d", 32)
	recoveryHash := sha256.Sum256([]byte(recoveryCookie))
	timestamp := now.Unix()
	exec(`INSERT INTO sessions(token_hash,user_id,password_digest,expires) VALUES($1,1,$2,$3)`, expiredCookieHash[:], digest[:], timestamp)
	exec(`INSERT INTO sessions(token_hash,user_id,password_digest,expires)
		SELECT sha256(convert_to('older-browser-'||i,'UTF8')),1,$1,$2
		FROM generate_series(1,$3::int) i`, digest[:], timestamp-1, credentialPruneBatch+2)
	exec(`INSERT INTO mcp_read_tokens(user_id,entity_id,token_hash,password_digest,issued_at,expires_at)
		VALUES(1,1,$1,$2,$3,$4)`, expiredKeyHash[:], digest[:], timestamp-600, timestamp)
	exec(`INSERT INTO admin_recovery_sessions(token_hash,user_id,password_digest,expires)
		VALUES($1,2,$2,$3),(sha256(convert_to('expired-recovery','UTF8')),2,$2,$4)`, recoveryHash[:], digest[:], timestamp+600, timestamp)
	exec(`INSERT INTO credential_prune_audit(utc_day,kind,removed) VALUES($1,'mcp_read_tokens',1)`,
		now.AddDate(0, 0, -credentialAuditDays-1).Format(time.DateOnly))

	expiredBrowser := []*http.Cookie{{Name: "sessionid", Value: expiredCookie}}
	call("GET", me, "", "", expiredBrowser, 401)
	call("GET", mcpMe, "", expiredKey, nil, 401)
	exec(`CREATE FUNCTION gomar.qa_block_credential_prune_audit() RETURNS trigger
		LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic interrupted prune'; END;$$;
		CREATE TRIGGER qa_block_credential_prune_audit BEFORE INSERT ON credential_prune_audit
		FOR EACH ROW WHEN (NEW.kind='sessions') EXECUTE FUNCTION gomar.qa_block_credential_prune_audit()`)
	if err := a.pruneCredentials(ctx, now); err == nil {
		t.Fatal("expected audit write to fail")
	}
	count := func(q string, want int, args ...any) {
		t.Helper()
		var actual int
		if err := db.QueryRowContext(ctx, q, args...).Scan(&actual); err != nil || actual != want {
			t.Fatalf("count %d, want %d: %v", actual, want, err)
		}
	}
	count("SELECT COUNT(*) FROM sessions WHERE expires<=$1", credentialPruneBatch+3, timestamp)
	count("SELECT COUNT(*) FROM credential_prune_audit WHERE kind='sessions'", 0)
	exec(`DROP TRIGGER qa_block_credential_prune_audit ON credential_prune_audit;
		DROP FUNCTION gomar.qa_block_credential_prune_audit()`)
	if err := a.pruneCredentials(ctx, now); err != nil {
		t.Fatal(err)
	}
	count("SELECT COUNT(*) FROM sessions WHERE expires<=$1", 3, timestamp)
	if err := a.pruneCredentials(ctx, now); err != nil {
		t.Fatal(err)
	}
	count("SELECT COUNT(*) FROM sessions WHERE expires<=$1", 0, timestamp)
	count("SELECT COUNT(*) FROM mcp_read_tokens WHERE expires_at<=$1", 0, timestamp)
	count("SELECT COUNT(*) FROM admin_recovery_sessions WHERE expires<=$1", 0, timestamp)
	count("SELECT removed FROM credential_prune_audit WHERE kind='sessions'", credentialPruneBatch+3)
	count("SELECT removed FROM credential_prune_audit WHERE kind='mcp_read_tokens'", 1)
	count("SELECT removed FROM credential_prune_audit WHERE kind='admin_recovery_sessions'", 1)
	call("GET", me, "", "", browser, 200)
	call("GET", me, "", "", expiredBrowser, 401)
	call("GET", mcpMe, "", activeKey, nil, 200)
	call("GET", mcpMe, "", expiredKey, nil, 401)
	admin := &app{db: db, adminRecovery: true}
	adminRequest := httptest.NewRequestWithContext(ctx, "GET", "https://example.com/admin/", nil)
	adminRequest.AddCookie(&http.Cookie{Name: adminRecoveryCookie, Value: recoveryCookie})
	if got, err := admin.currentAdminUser(nil, adminRequest); err != nil || got.id != 2 {
		t.Fatal("recovery session failed after prune", err)
	}

	exec(`INSERT INTO sessions(token_hash,user_id,password_digest,expires)
		SELECT sha256(convert_to('other-browser-'||i,'UTF8')),1,$1,$2
		FROM generate_series(1,$3::int) i`, digest[:], timestamp+600, browserSessionLimit-1)
	var csrfOnly []*http.Cookie
	for _, cookie := range browser {
		if cookie.Name == "csrftoken" {
			csrfOnly = append(csrfOnly, cookie)
		}
	}
	login := fmt.Sprintf(`{"usuario":"fixture-key-owner","clave":%q}`, password)
	call("POST", "/api/v1/auth/login", login, "", csrfOnly, 429)
	call("POST", "/api/v1/auth/login", login, "", browser, 200)
	count("SELECT COUNT(*) FROM sessions WHERE user_id=1", browserSessionLimit)

	exec(`INSERT INTO mcp_read_tokens(user_id,entity_id,token_hash,password_digest,issued_at,expires_at)
		SELECT 1,1,sha256(convert_to('other-read-key-'||i,'UTF8')),$1,$2,$3
		FROM generate_series(1,$4::int) i`, digest[:], timestamp-1, timestamp+600, mcpMemberKeyLimit-1)
	call("POST", askKey, `{"entity_id":1}`, "", browser, 429)
	readKey(2)
	exec(`INSERT INTO mcp_read_tokens(user_id,entity_id,token_hash,password_digest,issued_at,expires_at)
		SELECT 2,2,sha256(convert_to('tenant-read-key-'||i,'UTF8')),$1,$2,$3
		FROM generate_series(1,$4::int) i`, digest[:], timestamp-1, timestamp+600, mcpEntityKeyLimit-1)
	call("POST", askKey, `{"entity_id":2}`, "", browser, 429)
	call("GET", mcpMe, "", activeKey, nil, 200)
}
