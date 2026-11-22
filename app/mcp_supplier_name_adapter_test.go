package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeSupplierName(t *testing.T) {
	const raw = `{"supplier_id":7,"expected_revision":2,"name":"  Nuevo nombre  "}`
	in, err := decodeSupplierName([]byte(raw))
	if err != nil || in.ID != 7 || in.Revision != 2 || in.Name != "Nuevo nombre" {
		t.Fatal(in, err)
	}
	for _, body := range []string{
		strings.Replace(raw, `"expected_revision":2`, `"expected_revision":null`, 1),
		strings.Replace(raw, `"supplier_id":7`, `"supplier_id":7,"outlet_id":2`, 1),
		strings.Replace(raw, `"name":"  Nuevo nombre  "`, `"name":"A\u0000B"`, 1),
		strings.Replace(raw, `"name":"  Nuevo nombre  "`, `"name":"   "`, 1),
	} {
		if _, err := decodeSupplierName([]byte(body)); err == nil {
			t.Fatal("invalid supplier rename accepted")
		}
	}
	p := supplierNamePreview{Scope: documentReviewScope{1, 2, 3}, supplierNameInput: in, CurrentName: "Original", BusinessWide: true}
	wire, err := json.Marshal(p)
	if err != nil || !strings.Contains(string(wire), `"business_wide":true`) || strings.Contains(string(wire), "Scope") {
		t.Fatal(string(wire), err)
	}
}
