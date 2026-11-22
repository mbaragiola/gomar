package main

import (
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

//go:embed admin.html
var adminHTML string

var adminTemplate = template.Must(template.New("admin").Parse(adminHTML))

type adminList struct {
	Path, Label, Query string
}

var adminLists = []adminList{
	{documentAuditAdminPath, "Eventos de auditoría", ""},
	{adminLegalEntityPath, "Entidades legales", "SELECT name,cuit,id FROM entities ORDER BY name,id DESC"},
	{adminSupplierPath, "Proveedores", "SELECT name,'',id FROM suppliers ORDER BY name,id DESC"},
	{adminOutletPath, "Locales", `SELECT o.name,e.name,o.id FROM outlets o JOIN entities e ON e.id=o.entity_id
		ORDER BY o.name,o.id DESC`},
	{adminMembershipPath, "Membresías", `SELECT 'Membership object (' || m.id || ')',
		u.username || ' · ' || e.name || ' · ' || m.role,m.id FROM memberships m
		JOIN users u ON u.id=m.user_id JOIN entities e ON e.id=m.entity_id ORDER BY m.id DESC`},
	{"/admin/purchases/outletgrant/", "Permisos de local", `SELECT u.username,e.name || ' · ' || o.name FROM outlet_grants g
		JOIN outlets o ON o.id=g.outlet_id JOIN entities e ON e.id=o.entity_id
		JOIN memberships m ON m.id=g.membership_id JOIN users u ON u.id=m.user_id
		ORDER BY u.username,e.name,o.name,o.id`},
}

type adminCatalogRow struct {
	First, Second, Href string
}

type adminPage struct {
	Title, Token, Next, Username, Error string
	Login, LoggedOut                    bool
	Links                               []adminList
	Rows                                []adminCatalogRow
	CatalogDetail                       *adminCatalogFields
	OutletHistory                       *adminOutletHistoryPage
	Audit                               *adminAuditPage
	AuditDetail                         *adminAuditRow
	CatalogPager                        *adminCatalogPager
	CatalogAdd                          *adminCatalogAddPage
	AddCatalogPath, AddCatalogLabel     string
	SingleColumn                        bool
	PasswordForm                        *adminPasswordForm
	PasswordDone                        bool
}

func renderAdmin(w http.ResponseWriter, page adminPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	adminTemplate.Execute(w, page)
}

func (a *app) adminPostValid(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	return r.ParseForm() == nil && a.validAdminCSRF(r)
}

func redirectAdminLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/login/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
}

func (a *app) admin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Cookie")
	if a.adminRecovery && !effectiveHTTPS(r) {
		failure(w, badCSRF)
		return
	}
	path := r.URL.Path
	passwordPage := path == adminPasswordPath || path == adminPasswordDone
	var sessionCookies http.ResponseWriter
	if a.adminRecovery {
		sessionCookies = w
	}
	if passwordPage {
		w.Header().Set("Content-Language", "es-ar")
		// On non-POST unsafe methods Django checks the header, not form data.
		if !a.adminPostValid(w, r) || (r.Method != http.MethodPost && !safeMethod(r.Method) && !a.validAdminCSRF(r)) {
			failure(w, badCSRF)
			return
		}
		sessionCookies = w
	}
	u, err := a.currentAdminUser(sessionCookies, r)
	if err != nil {
		failure(w, err)
		return
	}
	if !passwordPage && !a.adminPostValid(w, r) {
		failure(w, badCSRF)
		return
	}
	if path == "/admin/login/" {
		a.adminLogin(w, r, u)
		return
	}
	if !u.superuser && !(a.adminRecovery && path == "/admin/logout/" && r.Method == http.MethodPost) {
		redirectAdminLogin(w, r)
		return
	}
	if passwordPage {
		a.serveAdminPassword(w, r, u)
		return
	}
	if path == "/admin/logout/" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := a.endAdminSession(w, r); err != nil {
			failure(w, err)
			return
		}
		renderAdmin(w, adminPage{Title: "Sesión cerrada", LoggedOut: true})
		return
	}
	if path != "/admin/" && !strings.HasPrefix(path, "/admin/purchases/") {
		http.NotFound(w, r)
		return
	}
	if path == adminLegalEntityAddPath {
		a.serveAdminLegalEntityAdd(w, r, adminPage{Token: a.ensureAdminCSRF(w, r), Username: u.name, Links: adminLists}, u.id)
		return
	}
	if path == adminOutletAddPath || path == adminSupplierAddPath {
		a.serveAdminCatalogAdd(w, r, adminPage{Token: a.ensureAdminCSRF(w, r), Username: u.name, Links: adminLists}, u.id, path == adminSupplierAddPath)
		return
	}
	if list, id, history := adminCatalogHistoryID(path); history {
		a.serveAdminCatalogHistory(w, r, adminPage{Token: a.ensureAdminCSRF(w, r), Username: u.name, Links: adminLists}, list, id)
		return
	}
	if list, id, detail := adminCatalogDetailID(path); detail {
		a.serveAdminCatalogDetail(w, r, adminPage{Token: a.ensureAdminCSRF(w, r), Username: u.name, Links: adminLists}, list, id, u.id)
		return
	}
	if id, detail := adminAuditDetailID(path); detail {
		a.serveAdminAuditDetail(w, r, adminPage{Token: a.ensureAdminCSRF(w, r), Username: u.name, Links: adminLists}, id)
		return
	}
	if path == documentAuditAdminPath {
		a.serveAdminAudit(w, r, adminPage{Token: a.ensureAdminCSRF(w, r), Username: u.name, Links: adminLists})
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		page := adminPage{Title: "Configuración", Token: a.ensureAdminCSRF(w, r), Username: u.name, Links: adminLists}
		if path == "/admin/" {
			renderAdmin(w, page)
			return
		}
		for _, list := range adminLists {
			if path != list.Path {
				continue
			}
			switch path {
			case adminOutletPath, adminLegalEntityPath, adminSupplierPath, adminMembershipPath:
				a.serveAdminCatalogList(w, r, page, list)
				return
			}
			page.Title = list.Label
			page.Rows, err = a.adminRows(r, list)
			if err != nil {
				failure(w, err)
				return
			}
			renderAdmin(w, page)
			return
		}
	}
	failure(w, unavailable)
}

func (a *app) adminRows(r *http.Request, list adminList, args ...any) ([]adminCatalogRow, error) {
	rows, err := a.db.QueryContext(r.Context(), list.Query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []adminCatalogRow{}
	for rows.Next() {
		var item adminCatalogRow
		if hasAdminCatalogDetail(list.Path) {
			var id int64
			if err := rows.Scan(&item.First, &item.Second, &id); err != nil {
				return nil, err
			}
			item.Href = list.Path + strconv.FormatInt(id, 10) + "/change/"
		} else if err := rows.Scan(&item.First, &item.Second); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *app) adminLogin(w http.ResponseWriter, r *http.Request, current user) {
	if r.Method == http.MethodGet && current.superuser {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	next := r.URL.Query().Get("next")
	if value := r.PostForm.Get("next"); value != "" {
		next = value
	}
	parsed, err := url.Parse(next)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || strings.ContainsAny(next, "\\\r\n") ||
		!strings.HasPrefix(parsed.Path, "/admin/") || strings.Contains(parsed.Path, "\\") {
		next = "/admin/"
	}
	page := adminPage{Title: "Iniciar sesión", Login: true, Next: next}
	if r.Method == http.MethodPost {
		page.Username = strings.TrimSpace(r.PostForm.Get("username"))
		u, err := a.authenticate(r.Context(), page.Username, r.PostForm.Get("password"))
		if err != nil {
			failure(w, err)
			return
		}
		if u.id != 0 && u.staff && (!a.adminRecovery || u.superuser) {
			if err := a.startAdminSession(w, r, u); err != nil {
				failure(w, err)
				return
			}
			http.Redirect(w, r, next, http.StatusFound)
			return
		}
		page.Error = "Ingresá un usuario y una contraseña válidos para una cuenta de administración."
	}
	page.Token = a.ensureAdminCSRF(w, r)
	renderAdmin(w, page)
}
