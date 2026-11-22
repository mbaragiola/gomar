package pedidosyapreview

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func load(t *testing.T, n string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + n + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestDispatchAndCancellationFixtures(t *testing.T) {
	raw := load(t, "dispatch")
	accepted, err := ParseDispatch("demo-centro", "demo-order-01", raw)
	if err != nil || accepted.Provider != "pedidosya" || accepted.Kind != "accepted" || accepted.StoreID != "demo-centro" ||
		accepted.OrderID != "demo-order-01" || len(accepted.EventID) != 64 ||
		!reflect.DeepEqual(accepted.Items, []Item{{"Sándwich de prueba", 2}, {"Agua sin gas", 1}}) {
		t.Fatal(accepted, err)
	}
	replay, err := ParseDispatch("demo-centro", "demo-order-01", raw)
	if err != nil || !reflect.DeepEqual(accepted, replay) {
		t.Fatal(replay, err)
	}
	cancelled, err := ParseCancellation("demo-centro", "demo-order-01", load(t, "cancellation"))
	if err != nil || cancelled.Kind != "cancelled" || cancelled.OrderID != accepted.OrderID ||
		cancelled.StoreID != accepted.StoreID || cancelled.EventID == accepted.EventID || len(cancelled.Items) != 0 {
		t.Fatal(cancelled, err)
	}
	other, err := ParseDispatch("demo-norte", "demo-order-01", raw)
	if err != nil || other.EventID == accepted.EventID {
		t.Fatal(other, err)
	}
}
func TestPreviewRejectsUnscopedOrUnsupportedInput(t *testing.T) {
	raw := string(load(t, "dispatch"))
	for _, p := range []string{
		strings.Replace(raw, `"test": false`, `"test": true`, 1),
		strings.Replace(raw, `"test": false`, `"test": null`, 1),
		strings.Replace(raw, `"quantity": "2"`, `"quantity": 2`, 1),
		strings.Replace(raw, `"quantity": "2"`, `"quantity": "0"`, 1),
		strings.Replace(raw, "demo-middleware-token-01", "not-a-demo", 1),
		strings.Replace(raw, "Sándwich de prueba", `salto\nlinea`, 1),
		strings.Repeat(" ", maxBody+1), raw + " {}", `{}`,
	} {
		ev, err := ParseDispatch("demo-centro", "demo-order-01", []byte(p))
		if !errors.Is(err, ErrFixture) || ev.Kind != "" {
			t.Fatal(ev, err)
		}
	}
	if _, err := ParseDispatch("live-centro", "demo-order-01", []byte(raw)); !errors.Is(err, ErrFixture) {
		t.Fatal(err)
	}
	if _, err := ParseCancellation("demo-centro", "live-order", load(t, "cancellation")); !errors.Is(err, ErrFixture) {
		t.Fatal(err)
	}
	if _, err := ParseCancellation("demo-centro", "demo-order-01", []byte(`{"status":"ORDER_PICKED_UP"}`)); !errors.Is(err, ErrFixture) {
		t.Fatal(err)
	}
}
