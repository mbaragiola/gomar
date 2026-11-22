package main

import (
	"net/http"
	"strconv"
)

const (
	adminOutletPath      = "/admin/purchases/outlet/"
	adminLegalEntityPath = "/admin/purchases/legalentity/"
	adminMembershipPath  = "/admin/purchases/membership/"
	adminSupplierPath    = "/admin/purchases/supplier/"
	adminCatalogPageSize = 100
)

type adminCatalogPager struct {
	Number, Previous, Next int
}

func (a *app) serveAdminCatalogList(w http.ResponseWriter, r *http.Request, page adminPage, list adminList) {
	number := 1
	if parsed, err := strconv.Atoi(r.URL.Query().Get("p")); err == nil {
		number = parsed
	}
	if number < 1 || number > int(^uint(0)>>1)/adminCatalogPageSize {
		http.Redirect(w, r, "?e=1", http.StatusFound)
		return
	}
	list.Query += " LIMIT $1 OFFSET $2"
	rows, err := a.adminRows(r, list, adminCatalogPageSize+1, (number-1)*adminCatalogPageSize)
	if err != nil {
		failure(w, err)
		return
	}
	if number > 1 && len(rows) == 0 {
		http.Redirect(w, r, "?e=1", http.StatusFound)
		return
	}
	pager := adminCatalogPager{Number: number}
	if number > 1 {
		pager.Previous = number - 1
	}
	if len(rows) > adminCatalogPageSize {
		rows = rows[:adminCatalogPageSize]
		pager.Next = number + 1
	}
	page.Title = list.Label
	page.Rows = rows
	switch list.Path {
	case adminLegalEntityPath:
		page.AddCatalogPath, page.AddCatalogLabel = adminLegalEntityAddPath, "Agregar entidad legal"
	case adminOutletPath:
		page.AddCatalogPath, page.AddCatalogLabel = adminOutletAddPath, "Añadir local"
	case adminSupplierPath:
		page.AddCatalogPath, page.AddCatalogLabel = adminSupplierAddPath, "Agregar proveedor"
	}
	page.CatalogPager = &pager
	page.SingleColumn = list.Path == adminSupplierPath
	renderAdmin(w, page)
}
