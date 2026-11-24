package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mbaragiola/gomar/app/frontendembed"
)

type app struct {
	reservationContacts bool
	db                  *sql.DB
	providerAuth        *hostedProviderAuth
	adminRecovery       bool
	providerOnly        bool
	providerEmployee    bool
	employeeOnboarding  bool
	employeePasswords   employeeAuthBudget
	mcpClock            func() time.Time
	posClock            func() time.Time

	host                                      string
	payablesClock                             func() time.Time
	reconciliationClock                       func() time.Time
	privateRoot                               *os.Root
	enableMCPSupplierNameProposals            bool
	enableMCPStockMinimumProposals            bool
	enableMCPStockTrackingProposals           bool
	enableMCPStockProductSetupProposals       bool
	enableMCPPOSCreateProposals               bool
	enableMCPReservationCancelProposals       bool
	enableMCPReservationConfirmProposals      bool
	enableMCPReservationTableAddProposals     bool
	enableMCPReservationSlotAddProposals      bool
	enableMCPReservationPolicyNoticeProposals bool
	enableMCPOutletDisplayLabelProposals      bool
	publicReservations                        bool
	enablePOS                                 bool
	enablePOSTableChecks                      bool
	enablePOSManualTender                     bool
	enablePOSTableFinalization                bool
	enableMCP                                 bool
	enableMCPWikiProposals                    bool
	enableMCPDocumentProposals                bool
	enableMCPWikiEditProposals                bool
	enableMCPStockReceiptProposals            bool
	enableMCPProposalSessions                 bool
	enableMCPDocumentEditProposals            bool
	enableMCPDocumentNumberProposals          bool
	enableMCPDocumentPointOfSaleProposals     bool
	enableMCPPOSCatalogProposals              bool
	enableMCPStockAdjustmentProposals         bool
	enableMCPDocumentReviewProposals          bool
	enableMCPReconciliationDraftProposals     bool
	enableDelivery                            bool
	enableStock                               bool
}

type problem struct {
	status int
	detail string
}

func (p problem) Error() string { return p.detail }

var (
	unauthenticated = problem{401, "Tenés que iniciar sesión."}
	badInput        = problem{422, "Los datos enviados no son válidos."}
	badJSON         = problem{400, "El cuerpo JSON no es válido."}
	badCSRF         = problem{403, csrfFailure}
	unavailable     = problem{503, "Esta operación todavía no está disponible en la versión experimental."}
)

func jsonResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func failure(w http.ResponseWriter, err error) {
	var issue problem
	if !errors.As(err, &issue) {
		issue = problem{500, "No se pudo completar la operación."}
	}
	jsonResponse(w, issue.status, map[string]string{"detail": issue.detail})
}

func parseJSON(w http.ResponseWriter, r *http.Request, target any) error {
	body, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	return decodeJSON(body, target)
}

func readJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		return nil, badJSON
	}
	return body, nil
}

func decodeJSON(body []byte, target any) error {
	if len(body) == 0 {
		return badInput
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		return badJSON
	}
	if err := json.Unmarshal(body, target); err != nil {
		return badInput
	}
	return nil
}

func safeMethod(method string) bool {
	return method == "GET" || method == "HEAD" || method == "OPTIONS" || method == "TRACE"
}

func (a *app) protected(next func(http.ResponseWriter, *http.Request, user) error) http.HandlerFunc {
	return a.protectedWithCache(next, false)
}

func (a *app) protectedWithCache(next func(http.ResponseWriter, *http.Request, user) error, reconciliation bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reconciliation {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Pragma", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("Vary", "Cookie")
		if !safeMethod(r.Method) && !validCSRF(r) {
			failure(w, badCSRF)
			return
		}
		u, err := currentUser(w, r, a.db)
		if err == nil {
			if u.id == 0 {
				err = unauthenticated
			} else if u.employee {
				err = employeeBrowserScope(r, u)
				if err == nil {
					err = next(w, r, u)
				}
			} else {
				err = next(w, r, u)
			}
		}
		if err != nil {
			failure(w, err)
		}
	}
}

func (a *app) apiLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.classicPasswordsDisabled() {
		detail := "Ingresá con Google o Microsoft."
		if a.managedEmployeesEnabled() {
			detail = "Ingresá con Google, Microsoft o tu cuenta de empleado."
		}
		failure(w, problem{403, detail})
		return
	}
	var input struct {
		Username *string `json:"usuario"`
		Password *string `json:"clave"`
	}
	if err := parseJSON(w, r, &input); err != nil {
		failure(w, err)
		return
	}
	if input.Username == nil || input.Password == nil {
		failure(w, badInput)
		return
	}
	if !validCSRF(r) {
		failure(w, badCSRF)
		return
	}
	u, err := a.authenticate(r.Context(), *input.Username, *input.Password)
	if err == nil && u.id == 0 {
		err = problem{401, incorrectLogin}
	}
	if err == nil {
		err = a.startSession(w, r, u)
	}
	if err != nil {
		failure(w, err)
		return
	}
	jsonResponse(w, 200, map[string]string{"usuario": u.name})
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	frontend := frontendembed.New()
	mux.Handle("/", frontend)
	a.registerPOSShell(mux, frontend)
	if a.publicReservations {
		mux.HandleFunc("GET /reservas/{venue}", func(w http.ResponseWriter, r *http.Request) {
			slug := r.PathValue("venue")
			if !reservationValidSlug(slug) {
				http.NotFound(w, r)
				return
			}
			request := r.Clone(r.Context())
			request.URL.Path = "/"
			request.URL.RawPath = ""
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			frontend.ServeHTTP(w, request)
		})
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		var health int
		if err := a.db.QueryRowContext(r.Context(), "SELECT 1").Scan(&health); err != nil {
			failure(w, err)
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "ok"})
	})
	apiRoutes := map[string]map[string]http.HandlerFunc{}
	addAPI := func(path, method string, handler http.HandlerFunc) {
		if apiRoutes[path] == nil {
			apiRoutes[path] = map[string]http.HandlerFunc{}
		}
		apiRoutes[path][method] = handler
	}
	a.registerKnowledge(addAPI)
	a.registerPOS(addAPI)
	a.registerDelivery(addAPI)
	a.registerStock(addAPI, a.enablePOS && a.enableStock)
	a.registerMCP(addAPI)
	a.registerProviderLogin(addAPI)
	a.registerMCPWikiProposals(addAPI)
	a.registerMCPDocumentProposals(addAPI)
	a.registerMCPWikiEditProposals(addAPI)
	a.registerMCPStockReceiptProposals(addAPI)
	a.registerNamedProposalSessions(addAPI)
	a.registerMCPPOSRenameProposals(addAPI)
	a.registerMCPDocumentReviewProposals(addAPI)
	a.registerMCPDocumentEditProposals(addAPI)
	a.registerMCPDocumentNumberProposals(addAPI)
	a.registerMCPDocumentPointOfSaleProposals(addAPI)
	a.registerMCPStockAdjustmentProposals(addAPI)
	a.registerMCPPOSCatalogProposals(addAPI)
	a.registerMCPReconciliationDraftProposals(addAPI)
	a.registerReservations(addAPI, a.publicReservations)
	a.registerMCPReservationConfirmProposals(addAPI)
	a.registerMCPReservationTableAddProposals(addAPI)
	a.registerMCPReservationSlotAddProposals(addAPI)
	a.registerMCPReservationPolicyNoticeProposals(addAPI)
	a.registerMCPOutletDisplayLabelProposals(addAPI)
	a.registerMCPReservationCancelProposals(addAPI)
	a.registerMCPPOSCreateProposals(addAPI)
	a.registerMCPStockMinimumProposals(addAPI)
	a.registerMCPStockTrackingProposals(addAPI)
	a.registerMCPStockProductSetupProposals(addAPI)
	a.registerMCPSupplierNameProposals(addAPI)
	a.registerReservationMenus(addAPI, a.publicReservations)
	unavailableOperation := func(_ *http.Request, _ *sql.Tx, _ entityMembership, _ []byte) (int, any, error) {
		return 0, nil, unavailable
	}
	pending := a.scoped("", unavailableOperation)
	addAPI("/api/v1/auth/csrf", "GET", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ensureCSRF(w, r)
		jsonResponse(w, 200, map[string]string{"detalle": "Cookie CSRF preparada."})
	})
	addAPI("/api/v1/auth/login", "POST", a.apiLogin)
	registerEmployeeOnboarding(a, addAPI)
	addAPI("/api/v1/auth/me", "GET", a.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
		jsonResponse(w, 200, map[string]string{"usuario": u.name})
		return nil
	}))
	addAPI("/api/v1/auth/logout", "POST", a.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
		if err := a.endSession(w, r); err != nil {
			return err
		}
		jsonResponse(w, 200, map[string]string{"detalle": "Sesión cerrada."})
		return nil
	}))
	addAPI("/api/v1/entities", "GET", a.protected(a.entities))
	addAPI("/api/v1/entities/{entity}/outlets", "GET", a.scoped("read_business_data", listOutlets))
	addAPI("/api/v1/entities/{entity}/suppliers", "GET", a.scoped("read_business_data", listSuppliers))
	addAPI("/api/v1/entities/{entity}/management", "GET", a.scoped("manage_entity_access", management))
	addAPI("/api/v1/entities/{entity}", "PUT", a.scopedMutation(updateEntity))
	addAPI("/api/v1/entities/{entity}/outlets", "POST", a.scopedMutation(saveOutlet))
	addAPI("/api/v1/entities/{entity}/outlets/{outlet}", "PUT", a.scopedMutation(saveOutlet))
	addAPI("/api/v1/entities/{entity}/memberships", "POST", a.scopedMutation(saveMembership))
	addAPI("/api/v1/entities/{entity}/memberships/{member}", "PUT", a.scopedMutation(saveMembership))
	addAPI("/api/v1/ocr/config", "GET", a.protected(ocrStatus))
	addAPI("/api/v1/entities/{entity}/outlets/{outlet}/intake", "POST", a.protected(a.uploadStagedIntake))
	addAPI("/api/v1/entities/{entity}/intake/{operation}/ocr", "POST", a.protected(a.stagedOCRDisabled))
	addAPI("/api/v1/entities/{entity}/intake/{operation}/download", "GET", a.protected(a.downloadStagedIntake))
	addAPI("/api/v1/entities/{entity}/intake/{operation}", "DELETE", a.protected(a.cancelStagedIntake))
	addAPI("/api/v1/entities/{entity}/suppliers", "POST", a.protected(a.createSupplier))
	addAPI("/api/v1/entities/{entity}/suppliers/{supplier}", "PUT", a.protected(a.updateSupplier))
	addAPI("/api/v1/entities/{entity}/documents", "GET", a.scoped("read_business_data", listDocuments))
	addAPI("/api/v1/entities/{entity}/documents/{document}", "GET", a.scoped("read_business_data", documentDetail))
	addAPI("/api/v1/entities/{entity}/outlets/{outlet}/documents/{document}/printed-reference-detail", "GET", a.protected(a.printedReferenceDocumentDetail))
	addAPI("/api/v1/entities/{entity}/outlets/{outlet}/documents/{document}/draft-queue-detail", "GET", a.protected(a.draftQueueDocumentDetail))
	addAPI("/api/v1/entities/{entity}/outlets/{outlet}/documents/{document}/attachment-handoff", "GET", a.protected(a.draftAttachmentHandoff))
	addAPI("/api/v1/entities/{entity}/documents/{document}", "PUT", a.protected(a.editDocument))
	addAPI("/api/v1/entities/{entity}/documents/{document}", "DELETE", a.protected(a.deleteDocument))
	addAPI("/api/v1/entities/{entity}/documents/{document}/review", "POST", a.protected(a.reviewDraft))
	addAPI("/api/v1/entities/{entity}/documents/{document}/attachments", "POST", a.protected(a.uploadDocumentImage))
	addAPI("/api/v1/entities/{entity}/documents/{document}/attachments/{attachment}", "GET", a.protected(a.downloadDocumentImage))
	addAPI("/api/v1/entities/{entity}/documents", "POST", a.protected(a.createDraft))
	addAPI("/api/v1/entities/{entity}/totals", "GET", a.scoped("read_business_data", documentTotals))
	addAPI("/api/v1/entities/{entity}/outlets/{outlet}/documents/{document}/payable-eligibility-handoff", "GET", a.protectedWithCache(a.payableEligibilityHandoff, true))
	addAPI("/api/v1/entities/{entity}/outlets/{outlet}/reconciliations/{run}/close-handoff", "GET", a.protectedWithCache(a.reconciliationCloseHandoff, true))
	addAPI("/api/v1/entities/{entity}/reconciliations", "GET", a.protectedWithCache(a.readReconciliation, true))
	addAPI("/api/v1/entities/{entity}/reconciliations", "POST", a.protected(a.createReconciliationDraft))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}", "GET", a.protectedWithCache(a.readReconciliation, true))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}", "DELETE", a.protectedWithCache(a.deleteReconciliationDraft, true))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}/inspect", "POST", a.protectedWithCache(a.inspectReconciliation, true))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}/preview", "POST", a.protectedWithCache(a.previewReconciliation, true))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}/sources", "POST", a.protectedWithCache(a.importReconciliationSource, true))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}/prepare", "POST", a.protectedWithCache(a.prepareReconciliation, true))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}/decisions", "POST", a.protectedWithCache(a.decideReconciliationPair, true))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}/close", "POST", a.protectedWithCache(a.closeReconciliationReview, true))
	addAPI("/api/v1/entities/{entity}/reconciliations/{run}/sources/{source}", "DELETE", a.protectedWithCache(a.removeReconciliationSource, true))
	for _, section := range []string{"rows", "candidates", "events"} {
		addAPI("/api/v1/entities/{entity}/reconciliations/{run}/"+section, "GET", a.protectedWithCache(
			func(w http.ResponseWriter, r *http.Request, u user) error {
				return a.readReconciliationSection(w, r, u, section)
			}, true))
	}
	a.registerPayables(addAPI)
	for path, methods := range apiRoutes {
		allowed := make([]string, 0, len(methods))
		for method := range methods {
			allowed = append(allowed, method)
		}
		slices.Sort(allowed)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if handler := methods[r.Method]; handler != nil {
				handler(w, r)
				return
			}
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusMethodNotAllowed)
			w.Write([]byte("Method not allowed"))
		})
	}
	mux.HandleFunc("/admin/", a.admin)
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.classicPasswordsDisabled() {
			r = providerOnlyRequest(r)
		}
		if a.providerEmployee {
			r = r.WithContext(context.WithValue(r.Context(), providerEmployeeContextKey{}, true))
		}
		if a.managedEmployeesEnabled() {
			r = r.WithContext(context.WithValue(r.Context(), employeeLoginContextKey{}, true))
		}
		w.Header().Set("X-Gomar-Experimental", "foundation")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Host != a.host {
			http.Error(w, "Invalid Host", 400)
			return
		}
		// With the asset fallback registered at /, ServeMux has a handler even
		// for unmatched API paths. Identify pending domain work before falling back.
		if _, pattern := mux.Handler(r); pattern == "/" && pendingAPI(r.URL.Path) {
			pending(w, withEntityPath(r))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func withEntityPath(r *http.Request) *http.Request {
	id, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/v1/entities/"), "/")
	r.SetPathValue("entity", id)
	return r
}

func pendingAPI(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/v1/entities/")
	if !ok {
		return false
	}
	_, rest, ok = strings.Cut(rest, "/")
	if !ok {
		return false
	}
	section, _, _ := strings.Cut(rest, "/")
	switch section {
	case "documents", "totals", "suppliers", "intake", "payables", "payments", "reconciliations":
		return true
	case "outlets":
		parts := strings.Split(rest, "/")
		return len(parts) == 3 && parts[2] == "intake"
	}
	return false
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0, badInput
	}
	return id, nil
}

func ocrStatus(w http.ResponseWriter, _ *http.Request, _ user) error {
	jsonResponse(w, 200, map[string]any{
		"habilitado": false, "proveedor": "Gemini API",
		"version_aviso": ocrDisclosureVersion,
		"aviso":         "Al usar OCR, el contenido del documento, los CUIT y los datos financieros se enviarán a Google mediante Gemini API. Google puede conservarlos hasta 55 días para controles de seguridad y detección de abuso. La carga sola no envía nada.",
		"mensaje":       disabledOCR.detail,
	})
	return nil
}
