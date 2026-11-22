package rappipreview

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestMappedReceivedAndCancellation(t *testing.T) {
	route := Route{"901001", "demo-rappi-centro"}
	raw := readFixture(t, "new-order")
	got, err := ParseNewOrder(route, raw)
	if err != nil || got.Kind != "received" || got.Provider != "rappi" || got.OrderID != "910001100" ||
		!reflect.DeepEqual(got.Items, []Item{{"Tostado de prueba", 2}, {"Agua mineral", 1}}) {
		t.Fatal(got, err)
	}
	again, err := ParseNewOrder(route, raw)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatal(again, err)
	}
	cancelled, err := ParseCancellation(route, readFixture(t, "cancel"))
	if err != nil || cancelled.Kind != "cancelled" || cancelled.OrderID != got.OrderID ||
		cancelled.StoreID != route.ExternalStoreID || cancelled.EventID == got.EventID || len(cancelled.Items) != 0 {
		t.Fatal(cancelled, err)
	}
}
func TestRejectsUnboundOrUnsupported(t *testing.T) {
	route := Route{"901001", "demo-rappi-centro"}
	got := string(readFixture(t, "new-order"))
	for _, raw := range []string{
		strings.Replace(got, "901001", "902001", 1),
		strings.Replace(got, "demo-rappi-centro", "another-venue", 1),
		strings.Replace(got, `"quantity": 2`, `"quantity": "2"`, 1),
		strings.Replace(got, `"quantity": 2`, `"quantity": 0`, 1),
		strings.Replace(got, "Tostado de prueba", `demo\norder`, 1),
		`{"action":"scheduled",` + strings.TrimPrefix(got, "{"),
		"[" + got + "]", got + " {}", strings.Repeat(" ", maxBody+1),
	} {
		if event, err := ParseNewOrder(route, []byte(raw)); !errors.Is(err, ErrFixture) || event.Kind != "" {
			t.Fatal(event, err)
		}
	}
	for _, raw := range []string{
		`{"event":"taken_visible_order","order_id":"910001100","store_id":"901001"}`,
		`{"event":"cancel_by_user","order_id":"910001100","store_id":"901002"}`,
		`{"event":"cancel_by_user","order_id":"910001100","store_id":901001}`,
	} {
		if _, err := ParseCancellation(route, []byte(raw)); !errors.Is(err, ErrFixture) {
			t.Fatal(err)
		}
	}
}
