package documentref

import (
	"net/url"
	"testing"
)

func TestExactPrintedReferenceQuery(t *testing.T) {
	for _, query := range []string{
		"number=2",
		"document_type=invoice&point_of_sale=1&number=2",
		"document_type=invoice&letter=&letter=A&point_of_sale=1&number=2",
		"document_type=invoice&letter=a&point_of_sale=1&number=2",
		"document_type=receipt&letter=A&point_of_sale=1&number=2",
		"document_type=invoice&letter=%20&point_of_sale=1&number=2",
		"document_type=invoice&letter=&point_of_sale=01&number=2",
		"document_type=invoice&letter=&point_of_sale=0&number=2",
		"document_type=invoice&letter=&point_of_sale=100000&number=2",
		"document_type=invoice&letter=&point_of_sale=1&number=%2B2",
		"document_type=invoice&letter=&point_of_sale=1&number=100000000",
	} {
		q, _ := url.ParseQuery(query)
		if _, ok := TakeQuery(q); ok {
			t.Errorf("%q was accepted", query)
		}
	}
	q := url.Values{"after_id": {"30"}}
	want := Reference{DocumentType: "debit_note", Letter: "", PointOfSale: 99999, Number: 99999999}
	want.AddQuery(q)
	got, ok := TakeQuery(q)
	if !ok || got == nil || *got != want || q.Encode() != "after_id=30" {
		t.Fatal(got, q, ok)
	}
	got, ok = TakeQuery(q)
	if !ok || got != nil {
		t.Fatal(got, ok)
	}
}
