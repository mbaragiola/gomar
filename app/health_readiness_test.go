package main

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthReadinessNeedsOpenDatabase(t *testing.T) {
	_, db := testPostgresDB(t)
	const origin = "http://127.0.0.1:8080"
	handler := (&app{db: db, host: "127.0.0.1:8080"}).routes()
	request := func(wantStatus int, wantBody string) {
		t.Helper()
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest(http.MethodGet, origin+"/healthz", nil))
		kind, _, err := mime.ParseMediaType(out.Header().Get("Content-Type"))
		if out.Code != wantStatus || err != nil || kind != "application/json" ||
			out.Body.String() != wantBody || out.Header().Get("Location") != "" ||
			len(out.Header().Values("Set-Cookie")) != 0 {
			t.Fatalf("unexpected readiness response: %d %v %q", out.Code, out.Header(), out.Body.String())
		}
	}
	request(http.StatusOK, "{\"status\":\"ok\"}\n")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	request(http.StatusInternalServerError, "{\"detail\":\"No se pudo completar la operación.\"}\n")
}
