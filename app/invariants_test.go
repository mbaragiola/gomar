package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMalformedDocumentQueries(t *testing.T) {
	for _, handler := range []scopedHandler{listDocuments, documentTotals} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.URL.RawQuery = "note=%"
		_, _, err := handler(request, nil, entityMembership{}, nil)
		if err != badInput {
			t.Fatalf("malformed query returned %v, want %v", err, badInput)
		}
	}
}

type observedBody struct {
	io.Reader
	observe func()
}

func (body observedBody) Read(p []byte) (int, error) {
	body.observe()
	return body.Reader.Read(p)
}

func TestScopedMutationReadsBodyBeforeTakingConnection(t *testing.T) {
	ctx, db := testPostgresDB(t)
	err := migratePostgres(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users(id,username,password,active,staff,superuser) VALUES (1,'tester','!synthetic',TRUE,FALSE,FALSE);
		INSERT INTO entities(id,name,cuit,active) VALUES (1,'Test Entity','00000000000',TRUE);
		INSERT INTO memberships(user_id,entity_id,role,active) VALUES (1,1,'administration',TRUE);`)
	if err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com"}
	login := httptest.NewRecorder()
	if err := a.startSession(login, httptest.NewRequest(http.MethodPost, "/", nil), user{id: 1, password: "!synthetic"}); err != nil {
		t.Fatal(err)
	}
	body := observedBody{strings.NewReader(`{"nombre":"Updated Entity","cuit":"00000000000"}`), func() {
		if db.Stats().InUse != 0 {
			t.Error("request body read while a database connection was checked out")
		}
	}}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/entities/1", body)
	for _, cookie := range login.Result().Cookies() {
		request.AddCookie(cookie)
		if cookie.Name == "csrftoken" {
			request.Header.Set("X-CSRFToken", cookie.Value)
		}
	}
	response := httptest.NewRecorder()
	a.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update returned %d: %s", response.Code, response.Body)
	}
	var name string
	if err := db.QueryRow("SELECT name FROM entities WHERE id=1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Updated Entity" {
		t.Fatalf("stored entity name = %q", name)
	}
}
