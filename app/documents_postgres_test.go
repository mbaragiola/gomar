package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const nativeDocumentOCRAnnotation = `{"supplier_legal_name":"solo sugerencia sintética","issuer_cuit":"30-71111111-1","recipient_cuit":"30-11111111-8","document_type":" Factura ","letter":"a","point_of_sale":4,"number":88,"issue_date":"2026-10-08","due_date":null,"currency":"ars","subtotal":null,"tax":null,"total":"1500.00","tax_breakdown":null}`

func nativeDocumentPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func nativeDocumentUpload(t *testing.T, path, name string, data []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="archivo"; filename="` + name + `"`},
		"Content-Type":        {"image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, &buf)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return r
}

func nativeDocumentSession(t *testing.T, a *app, routes http.Handler, actor int64) []*http.Cookie {
	t.Helper()
	login := httptest.NewRecorder()
	if err := a.startSession(login, httptest.NewRequest("POST", "/api/v1/auth/login", nil), user{id: actor, password: "!synthetic"}); err != nil {
		t.Fatal(err)
	}
	prep := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/v1/auth/csrf", nil)
	for _, cookie := range login.Result().Cookies() {
		request.AddCookie(cookie)
	}
	routes.ServeHTTP(prep, request)
	if prep.Code != 200 {
		t.Fatal(prep.Body.String())
	}
	return append(login.Result().Cookies(), prep.Result().Cookies()...)
}

func TestDocumentPostgresConflictAndUncertainCommit(t *testing.T) {
	for _, test := range []struct {
		err              error
		conflict, retain bool
	}{
		{&pgconn.PgError{Code: "23505", ConstraintName: "documents_identity_unique"}, true, false},
		{&pgconn.PgError{Code: "23505", ConstraintName: "document_attachments_storage_name_key"}, false, false},
		{&pgconn.PgError{Code: "40001"}, false, false},
		{pgx.ErrTxCommitRollback, false, false},
		{io.EOF, false, true},
		{&pgconn.PgError{Code: "57P01"}, false, true},
	} {
		a := app{}
		if isDocumentIdentityConflict(test.err) != test.conflict || a.retainAfterDocumentCommitError(test.err) != test.retain {
			t.Fatal(test.err)
		}
	}
}

func TestPostgresDocumentsPrivateAttachmentAndIntake(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const actor = 724002
	const supplier = "30711111111"
	for _, stmt := range []string{
		`INSERT INTO entities(id,name,cuit,active) VALUES(724002,'Synthetic document entity','30111111118',true)`,
		`INSERT INTO outlets(id,entity_id,name,active) VALUES(724002,724002,'Synthetic document outlet',true)`,
		`INSERT INTO users(id,username,password,active,staff,superuser) VALUES(724002,'document-test-pg','!synthetic',true,false,false)`,
		`INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(724002,724002,724002,'administration',true)`,
		`INSERT INTO outlet_grants(membership_id,outlet_id) VALUES(724002,724002)`,
		`INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(724002,724002,'Synthetic supplier','30711111111',true)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	media := t.TempDir()
	root, err := os.OpenRoot(media)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	a := &app{db: db, host: "example.com", privateRoot: root}
	// Route registration is tested separately; these are the production
	// handlers under their normal protected wrappers.
	mux := http.NewServeMux()
	const e = "/api/v1/entities/724002"
	mux.HandleFunc("GET /api/v1/auth/csrf", func(w http.ResponseWriter, r *http.Request) { ensureCSRF(w, r); w.WriteHeader(200) })
	mux.HandleFunc("POST "+e+"/documents", a.protected(a.createDraft))
	mux.HandleFunc("GET "+e+"/documents", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("entity", "724002")
		a.scoped("read_business_data", listDocuments)(w, r)
	})
	mux.HandleFunc("POST "+e+"/documents/{document}/attachments", a.protected(a.uploadDocumentImage))
	mux.HandleFunc("GET "+e+"/documents/{document}/attachments/{attachment}", a.protected(a.downloadDocumentImage))
	mux.HandleFunc("POST "+e+"/documents/{document}/review", a.protected(a.reviewDraft))
	mux.HandleFunc("POST "+e+"/outlets/{outlet}/intake", a.protected(a.uploadStagedIntake))
	mux.HandleFunc("GET "+e+"/intake/{operation}/download", a.protected(a.downloadStagedIntake))
	mux.HandleFunc("DELETE "+e+"/intake/{operation}", a.protected(a.cancelStagedIntake))
	mux.HandleFunc("POST "+e+"/intake/{operation}/ocr", a.protected(a.stagedOCRDisabled))
	mux.HandleFunc("POST "+e+"/intake/{operation}/synthetic-ocr", a.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
		return a.stagedOCRDraft(w, r, u, func(context.Context, []byte, string) ([]byte, error) { return []byte(nativeDocumentOCRAnnotation), nil })
	}))
	mux.HandleFunc("POST "+e+"/intake/{operation}/synthetic-confirm", a.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
		return a.confirmStagedPNG(w, r, u, func(context.Context, string) error {
			return errors.New("synthetic deferred intake cleanup")
		})
	}))
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("entity", "724002"); mux.ServeHTTP(w, r) })
	cookies := nativeDocumentSession(t, a, routes, actor)
	invoke := func(req *http.Request, auth bool, expected int) *httptest.ResponseRecorder {
		t.Helper()
		if auth {
			for _, c := range cookies {
				req.AddCookie(c)
				if c.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		result := httptest.NewRecorder()
		routes.ServeHTTP(result, req)
		if result.Code != expected {
			t.Fatalf("%s %s returned %d: %s", req.Method, req.URL.Path, result.Code, result.Body)
		}
		return result
	}
	request := func(method, path, data string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(data)).WithContext(ctx)
		if data != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		return r
	}
	body := `{"outlet_id":724002,"supplier_id":724002,"issuer_cuit":"` + supplier + `","tipo":"invoice","letra":"A","punto_venta":3,"numero":40,"moneda":"ARS","fecha_emision":"2026-10-05","neto":"100.10","iva":"21.02","total":"121.12","descripcion":"Synthetic document"}`
	primary := invoke(request("POST", e+"/documents", body), true, 201)
	var first documentView
	if err := json.Unmarshal(primary.Body.Bytes(), &first); err != nil || first.ID < 1 {
		t.Fatal(first, err)
	}
	invoke(request("POST", e+"/documents", body), true, 409)
	invoke(request("GET", e+"/documents", ""), true, 200)
	invoke(request("GET", e+"/documents?outlet_id=724002", ""), true, 200)
	document := fmt.Sprintf("%s/documents/%d", e, first.ID)
	png := nativeDocumentPNG(t)
	// Force a constraint failure on new attachment rows to exercise the
	// SQL rollback and the private file's compensating unlink.
	if _, err := db.ExecContext(ctx, `ALTER TABLE document_attachments ADD CONSTRAINT doc_qa_refuse CHECK(id<0) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	invoke(nativeDocumentUpload(t, document+"/attachments", "synthetic.png", png), true, 500)
	if files, err := os.ReadDir(media); err != nil || len(files) != 0 {
		t.Fatal(files, err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE document_attachments DROP CONSTRAINT doc_qa_refuse`); err != nil {
		t.Fatal(err)
	}
	saved := invoke(nativeDocumentUpload(t, document+"/attachments", "synthetic.png", png), true, 201)
	var attachment documentAttachment
	if err := json.Unmarshal(saved.Body.Bytes(), &attachment); err != nil || attachment.ID < 1 {
		t.Fatal(attachment, err)
	}
	attachmentURL := fmt.Sprintf("%s/attachments/%d", document, attachment.ID)
	if got := invoke(request("GET", attachmentURL, ""), true, 200); !bytes.Equal(got.Body.Bytes(), png) {
		t.Fatal("private PDF/image bytes differed")
	}
	invoke(request("GET", attachmentURL, ""), false, 401)
	invoke(request("POST", document+"/review", `{"motivo":"Revisión de prueba","expected_revision":1}`), true, 200)
	invoke(request("POST", document+"/review", `{"motivo":"Segundo envío","expected_revision":1}`), true, 409)
	stage := invoke(nativeDocumentUpload(t, e+"/outlets/724002/intake", "synthetic-stage.png", png), true, 201)
	var receipt intakeReceipt
	if err := json.Unmarshal(stage.Body.Bytes(), &receipt); err != nil || receipt.ID == "" {
		t.Fatal(receipt, err)
	}
	stagedURL := e + "/intake/" + receipt.ID
	invoke(request("GET", stagedURL+"/download", ""), false, 401)
	got := invoke(request("GET", stagedURL+"/download", ""), true, 200)
	if !bytes.Equal(got.Body.Bytes(), png) {
		t.Fatal("staged image differed")
	}
	consent := `{"acepta_envio_google":true,"version_aviso":"` + ocrDisclosureVersion + `"}`
	invoke(request("POST", stagedURL+"/ocr", consent), true, 503)
	var deniedConsent int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ocr_consent_events WHERE operation_id=$1`, receipt.ID).Scan(&deniedConsent); err != nil || deniedConsent != 0 {
		t.Fatal(deniedConsent, err)
	}
	invoke(request("POST", stagedURL+"/synthetic-ocr", consent), true, 200)
	var stagedStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM intake_operations WHERE id=$1`, receipt.ID).Scan(&stagedStatus); err != nil || stagedStatus != "ready" {
		t.Fatal(stagedStatus, err)
	}
	confirmed := invoke(request("POST", stagedURL+"/synthetic-confirm", strings.Replace(body, `"numero":40`, `"numero":41`, 1)), true, 201)
	var confirmedDoc documentView
	if err := json.Unmarshal(confirmed.Body.Bytes(), &confirmedDoc); err != nil || confirmedDoc.ID == first.ID || confirmedDoc.ID < 1 || len(confirmedDoc.Attachments) != 1 {
		t.Fatal(confirmedDoc, err)
	}
	confirmedStage := receipt.ID
	if _, err := os.Stat(media + "/" + intakeStorage(confirmedStage)); err != nil {
		t.Fatal("the confirmed staged source was not retained after cleanup failure", err)
	}
	var confirmedStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM intake_operations WHERE id=$1`, receipt.ID).Scan(&confirmedStatus); err != nil || confirmedStatus != "confirmed" {
		t.Fatal(confirmedStatus, err)
	}
	stage = invoke(nativeDocumentUpload(t, e+"/outlets/724002/intake", "synthetic-cancel.png", png), true, 201)
	if err := json.Unmarshal(stage.Body.Bytes(), &receipt); err != nil || receipt.ID == "" {
		t.Fatal(receipt, err)
	}
	stagedURL = e + "/intake/" + receipt.ID

	invoke(request("DELETE", stagedURL, ""), true, 200)
	if _, err := os.Stat(media + "/intake-" + receipt.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	stage = invoke(nativeDocumentUpload(t, e+"/outlets/724002/intake", "synthetic-expiry.png", png), true, 201)
	var expired, fresh intakeReceipt
	if err := json.Unmarshal(stage.Body.Bytes(), &expired); err != nil || expired.ID == "" {
		t.Fatal(expired, err)
	}
	stage = invoke(nativeDocumentUpload(t, e+"/outlets/724002/intake", "synthetic-live.png", png), true, 201)
	if err := json.Unmarshal(stage.Body.Bytes(), &fresh); err != nil || fresh.ID == "" {
		t.Fatal(fresh, err)
	}
	now := time.Now().UnixMicro()
	if _, err := db.ExecContext(ctx, `UPDATE intake_operations SET created_at=$2-86400000001,expires_at=$2-1 WHERE id=$1`, expired.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM outlet_grants WHERE membership_id=724002 AND outlet_id=724002;
		CREATE FUNCTION gomar.qa_fail_expired_reference_clear() RETURNS trigger
		LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic interrupted reference clear'; END;$$;
		CREATE TRIGGER qa_fail_expired_reference_clear BEFORE UPDATE OF storage_name ON intake_operations
		FOR EACH ROW WHEN (OLD.status='expired' AND NEW.storage_name IS NULL)
		EXECUTE FUNCTION gomar.qa_fail_expired_reference_clear()`); err != nil {
		t.Fatal(err)
	}
	expiredURL := e + "/intake/" + expired.ID + "/download"
	invoke(request("GET", expiredURL, ""), true, 404)
	if _, err := a.sweepStagedIntake(ctx, "", now); err == nil {
		t.Fatal("expected interruption after file removal")
	}
	var status, retained string
	if err := db.QueryRowContext(ctx, `SELECT status,COALESCE(storage_name,'') FROM intake_operations WHERE id=$1`, expired.ID).
		Scan(&status, &retained); err != nil || status != "expired" || retained != intakeStorage(expired.ID) {
		t.Fatalf("expiry transition or cleanup reference lost: %s %q %v", status, retained, err)
	}
	for _, operation := range []string{expired.ID, confirmedStage} {
		if _, err := os.Stat(media + "/" + intakeStorage(operation)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected staged source removed: %v", err)
		}
	}
	if _, err := os.Stat(media + "/" + intakeStorage(fresh.ID)); err != nil {
		t.Fatal("live staged source was touched", err)
	}
	if _, err := db.ExecContext(ctx, `
		DROP TRIGGER qa_fail_expired_reference_clear ON intake_operations;
		DROP FUNCTION gomar.qa_fail_expired_reference_clear()`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.sweepStagedIntake(ctx, "", now); err != nil {
		t.Fatal("could not retry expiry after missing file", err)
	}
	var preserved bool
	if err := db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM intake_operations WHERE id=$1 AND status='expired' AND storage_name IS NULL)
		AND EXISTS(SELECT 1 FROM intake_operations WHERE id=$2 AND status='confirmed' AND storage_name IS NULL)
		AND (SELECT count(*) FROM ocr_consent_events WHERE operation_id=$2)=1
		AND (SELECT count(*) FROM document_audit WHERE document_id=$3)=2`, expired.ID, confirmedStage, confirmedDoc.ID).
		Scan(&preserved); err != nil || !preserved {
		t.Fatalf("sweep did not preserve metadata and confirmed audit: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO outlet_grants VALUES(724002,724002)`); err != nil {
		t.Fatal(err)
	}
	invoke(request("GET", expiredURL, ""), true, 410)
	confirmedURL := fmt.Sprintf("%s/documents/%d/attachments/%d", e, confirmedDoc.ID, confirmedDoc.Attachments[0].ID)
	if got := invoke(request("GET", confirmedURL, ""), true, 200); !bytes.Equal(got.Body.Bytes(), png) {
		t.Fatal("confirmed attachment was modified during the staged sweep")
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO intake_operations
		(id,entity_id,outlet_id,user_id,status,mime_type,size,digest,storage_name,created_at,expires_at)
		SELECT id,724002,724002,$1,'expired','image/png',1,repeat('0',64),'intake-'||id,1,2 FROM (
		 SELECT '00001111-0000-4000-8000-'||lpad(i::text,12,'0') AS id FROM generate_series(1,$2::int) i
		) rows`, actor, intakeEntityFiles-1); err != nil {
		t.Fatal(err)
	}
	beforeFiles, err := os.ReadDir(media)
	if err != nil {
		t.Fatal(err)
	}
	invoke(nativeDocumentUpload(t, e+"/outlets/724002/intake", "synthetic-over-budget.png", png), true, 409)
	if afterFiles, err := os.ReadDir(media); err != nil || len(afterFiles) != len(beforeFiles) {
		t.Fatalf("over-budget upload wrote a private file: %v", err)
	}
	var countAudit, countConsent int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM document_audit WHERE document_id=$1),(SELECT count(*) FROM ocr_consent_events WHERE operation_id=$2)`, first.ID, receipt.ID).Scan(&countAudit, &countConsent); err != nil || countAudit != 3 || countConsent != 0 {
		t.Fatal(countAudit, countConsent, err)
	}
}
