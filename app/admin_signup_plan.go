package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const adminSignupPlanPath = "/admin/alta/plan/"

const adminSignupPlanLifetime = 2 * time.Minute

// Explicit fixture configuration only. No environment, flag, startup
// wiring, or header can install this writer. The ephemeral stages are
// neither authorization nor durable signup records.
type adminSignupPlanFixture struct {
	audience string
	mu       sync.Mutex
	tickets  map[string]adminSignupPlanTicket
}

type adminSignupPlanTicket struct {
	session [32]byte
	target  nativeOwnerTarget
	expires time.Time
	review  *nativeSignupPlan
}

func (f *adminSignupPlanFixture) issue(ticket adminSignupPlanTicket) (string, error) {
	now := nativeOwnerNow()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tickets == nil {
		f.tickets = make(map[string]adminSignupPlanTicket)
	}
	for key, prior := range f.tickets {
		if !now.Before(prior.expires) || prior.session == ticket.session {
			delete(f.tickets, key)
		}
	}
	if len(f.tickets) >= 64 || !now.Before(ticket.expires) {
		return "", nativeOwnerConsentUnavailable
	}
	key := newProviderRandom()
	f.tickets[key] = ticket
	return key, nil
}

func (f *adminSignupPlanFixture) take(key string, session [32]byte, reviewed bool) (adminSignupPlanTicket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ticket, ok := f.tickets[key]
	if !ok || (ticket.review != nil) != reviewed || subtle.ConstantTimeCompare(ticket.session[:], session[:]) != 1 {
		return adminSignupPlanTicket{}, nativeOwnerConsentUnavailable
	}
	if !nativeOwnerNow().Before(ticket.expires) {
		delete(f.tickets, key)
		return adminSignupPlanTicket{}, nativeOwnerConsentUnavailable
	}
	delete(f.tickets, key)
	return ticket, nil
}

type adminSignupPlanPage struct {
	Phase, Ticket, Organization string
	Slots                       []adminSignupPlanSlot
}

type adminSignupPlanSlot struct {
	Field, Ref, Label string
}

func renderAdminSignupPlan(w http.ResponseWriter, a *app, r *http.Request, phase, key string, ticket adminSignupPlanTicket) {
	page := adminSignupPlanPage{Phase: phase, Ticket: key}
	if phase == "labels" || phase == "review" {
		for i, ref := range ticket.target.Outlets {
			slot := adminSignupPlanSlot{Field: "branch_" + strconv.Itoa(i), Ref: ref}
			if ticket.review != nil {
				slot.Label = ticket.review.Branches[i].Name
			}
			page.Slots = append(page.Slots, slot)
		}
		if ticket.review != nil {
			page.Organization = ticket.review.Organization
		}
	}
	renderAdmin(w, adminPage{Title: "Preparar el alta", Token: a.ensureAdminCSRF(w, r), SignupPlan: &page})
}

func signupPlanFields(form url.Values, fields ...string) bool {
	if len(form) != len(fields)+2 { // action and CSRF are always required.
		return false
	}
	for _, field := range fields {
		if len(form[field]) != 1 {
			return false
		}
	}
	return true
}

// HTML form transport, with a single unambiguous body and single source
// for CSRF. Adapt the validated form field to the existing native check.
func readAdminSignupPlan(w http.ResponseWriter, r *http.Request) error {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" || len(params) != 0 ||
		len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("X-CSRFToken")) != 0 {
		return badInput
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	if r.ParseForm() != nil || len(r.PostForm["csrfmiddlewaretoken"]) != 1 || len(r.PostForm["action"]) != 1 {
		return badInput
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			return badInput
		}
	}
	r.Header.Set("X-CSRFToken", r.PostForm.Get("csrfmiddlewaretoken"))
	return nil
}

func (a *app) adminSignupCase(r *http.Request, session [32]byte, ref string) (adminSignupPlanTicket, error) {
	var ticket adminSignupPlanTicket
	err := a.nativeOwnerRecoveryTransaction(r, func(ctx context.Context, tx *sql.Tx, operator, sessionExpiry int64) error {
		var intent string
		if err := tx.QueryRowContext(ctx, `SELECT intent FROM gomar.native_owner_designations WHERE request_ref=$1`, ref).Scan(&intent); err != nil {
			return err
		}
		d, err := lockCurrentNativeOwnerDesignation(ctx, tx, intent)
		if err != nil {
			return err
		}
		var available bool
		// Do not lock the claimant after the intent. The final #043 writer
		// rechecks the claimant and holds its lock before the intent.
		err = tx.QueryRowContext(ctx, `SELECT
			EXISTS (SELECT 1 FROM gomar.users u JOIN gomar.provider_identities p ON p.user_id=u.id
			 WHERE u.id=$3 AND u.active AND NOT u.staff AND NOT u.superuser AND u.password=$4)
			AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_receipts WHERE intent=$1)
			AND NOT EXISTS (SELECT 1 FROM gomar.native_signup_plans WHERE intent=$1 AND revision=$2)`,
			intent, d.Target.Revision, d.Actor, providerOnlyPassword).Scan(&available)
		if err != nil {
			return err
		}
		now := nativeOwnerNow()
		if !available || d.Actor == operator || d.Target.RequestRef != ref || d.Target.Audience != a.signupPlanAdmin.audience ||
			!d.approvedAt(now) || now.Unix() >= sessionExpiry {
			return nativeOwnerConsentUnavailable
		}
		ticket = adminSignupPlanTicket{session: session, target: d.Target,
			expires: nativeOwnerDeadline(now.Add(adminSignupPlanLifetime), d.Expires, d.ApprovalExpires.Time, time.Unix(sessionExpiry, 0))}
		return nil
	})
	return ticket, err
}

func (a *app) serveAdminSignupPlan(w http.ResponseWriter, r *http.Request) {
	nativeSignupPrivateHeaders(w)
	// A navigation form carries Origin: null under no-referrer in Chrome.
	// Allow referrers solely within this origin; the address has no case data.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Language", "es-AR")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	f := a.signupPlanAdmin
	if f == nil || !a.adminRecovery || a.db == nil || !nativeOwnerOpaqueID(f.audience) || !isPrivateHTTPS(r) ||
		r.Host != a.host || r.URL.Path != adminSignupPlanPath || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery ||
		len(r.Header.Values("Authorization")) != 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		cookies := r.CookiesNamed(adminRecoveryCookie)
		if len(cookies) != 1 || len(cookies[0].Value) != 32 {
			http.NotFound(w, r)
			return
		}
		tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			failure(w, err)
			return
		}
		defer tx.Rollback()
		_, expires, err := lockedNativeOwnerOperator(r.Context(), tx, sha256.Sum256([]byte(cookies[0].Value)))
		if err != nil || nativeOwnerNow().Unix() >= expires {
			http.NotFound(w, r)
			return
		}
		if err = tx.Commit(); err != nil {
			failure(w, err)
			return
		}
		renderAdminSignupPlan(w, a, r, "case", "", adminSignupPlanTicket{})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := readAdminSignupPlan(w, r); err != nil {
		failure(w, err)
		return
	}
	session, err := a.nativeOwnerRecoverySession(r)
	if err == nil {
		err = a.adminSignupPlanAction(w, r, f, session)
	}
	if err != nil {
		failure(w, err)
	}
}

func (a *app) adminSignupPlanAction(w http.ResponseWriter, r *http.Request, f *adminSignupPlanFixture, session [32]byte) error {
	form := r.PostForm
	action := form.Get("action")
	if action == "open" {
		if !signupPlanFields(form, "case") || !nativeOwnerOpaqueID(form.Get("case")) {
			return badInput
		}
		ticket, err := a.adminSignupCase(r, session, form.Get("case"))
		if err != nil {
			return err
		}
		key, err := f.issue(ticket)
		if err != nil {
			return err
		}
		renderAdminSignupPlan(w, a, r, "labels", key, ticket)
		return nil
	}
	key := form.Get("ticket")
	if (action != "review" && action != "save") || !providerRandom(key) ||
		(action == "save" && (!signupPlanFields(form, "ticket", "confirm") || form.Get("confirm") != "yes")) {
		return badInput
	}
	ticket, err := f.take(key, session, action == "save")
	if err != nil {
		return err
	}
	if action == "save" {
		// No form field can substitute the scope or display. The writer
		// rechecks the recovery realm, claimant, current approval and scope.
		if err = a.recordNativeSignupPlanBefore(r, ticket.target, *ticket.review, ticket.expires); err != nil {
			return err
		}
		renderAdminSignupPlan(w, a, r, "saved", "", adminSignupPlanTicket{})
		return nil
	}
	fields := []string{"ticket", "organization"}
	plan := nativeSignupPlan{Organization: form.Get("organization")}
	for i, ref := range ticket.target.Outlets {
		field := "branch_" + strconv.Itoa(i)
		fields = append(fields, field)
		plan.Branches = append(plan.Branches, nativeInstallBranch{Ref: ref, Name: form.Get(field)})
	}
	if !signupPlanFields(form, fields...) {
		return badInput
	}
	var valid bool
	plan.Branches, valid = canonicalNativeOwnerDisplay(ticket.target, plan.Organization, plan.Branches)
	if !valid {
		return badInput
	}
	current, err := a.adminSignupCase(r, session, ticket.target.RequestRef)
	if err != nil {
		return err
	}
	if !sameNativeOwnerTarget(current.target, ticket.target) {
		return nativeOwnerConsentUnavailable
	}
	ticket.expires = nativeOwnerDeadline(ticket.expires, current.expires)
	ticket.review = &plan
	key, err = f.issue(ticket)
	if err != nil {
		return err
	}
	renderAdminSignupPlan(w, a, r, "review", key, ticket)
	return nil
}
