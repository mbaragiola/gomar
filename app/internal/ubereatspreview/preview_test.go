package ubereatspreview

import (
	"bytes"
	"os"
	"testing"
)

func readDemo(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMarketplaceFixturesProjectOnlyRequiredFields(t *testing.T) {
	notification := readDemo(t, "notification")
	details := readDemo(t, "details")
	got, err := ParseAccepted(notification, details)
	if err != nil || got.Kind != "accepted" || got.StoreID != "00000000-0000-4000-8000-000000000201" ||
		got.OrderID != "00000000-0000-4000-8000-000000000101" || got.EventID != "00000000-0000-4000-8000-000000000301" ||
		len(got.Items) != 1 || got.Items[0] != (Item{"Tostado de prueba", 2}) {
		t.Fatalf("projected accepted: %+v, %v", got, err)
	}
	cancelled, err := ParseCancellation(readDemo(t, "cancellation"))
	if err != nil || cancelled.Kind != "cancelled" || got.EventID == cancelled.EventID ||
		got.StoreID != cancelled.StoreID || got.OrderID != cancelled.OrderID || len(cancelled.Items) != 0 {
		t.Fatalf("projected cancelled: %+v, %v", cancelled, err)
	}
}

func TestMarketplaceNoticeCannotAcceptUnboundOrder(t *testing.T) {
	n := readDemo(t, "notification")
	d := readDemo(t, "details")
	for _, c := range []struct{ name, old, replacement string }{
		{"created", "ACCEPTED", "CREATED"}, {"denied", "ACCEPTED", "DENIED"}, {"finished", "ACCEPTED", "FINISHED"},
		{"cancelled", "ACCEPTED", "CANCELED"}, {"store", "000000000201", "000000000202"}, {"order", "000000000101", "000000000102"},
		{"nonmarketplace", "UBER_EATS", "POSTMATES"}, {"nonpositive", "quantity\":2", "quantity\":0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseAccepted(n, bytes.Replace(d, []byte(c.old), []byte(c.replacement), 1)); err != ErrFixture {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := ParseAccepted(n, nil); err != ErrFixture {
		t.Fatal(err)
	}
	if _, err := ParseAccepted(bytes.Replace(n, []byte("00000000-0000-4000"), []byte("11111111-0000-4000"), 1), d); err != ErrFixture {
		t.Fatal(err)
	}
	if _, err := ParseAccepted(append(n, make([]byte, 1<<15)...), d); err != ErrFixture {
		t.Fatal(err)
	}
	if _, err := ParseCancellation(bytes.Replace(readDemo(t, "cancellation"), []byte("orders.cancel"), []byte("orders.failure"), 1)); err != ErrFixture {
		t.Fatal(err)
	}
}
