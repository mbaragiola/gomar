package postender

import (
	"fmt"
	"math"
	"testing"
)

var check = Scope{11, 21, 31}

func request(actor int64, n uint64) Request {
	return Request{check, actor, fmt.Sprintf("00000000-0000-4000-8000-%012x", n)}
}

func journal(t testing.TB, total int64) *Journal {
	t.Helper()
	j, err := New(check, total)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func result(t testing.TB, fn func() (bool, error), wantReplay bool, wantErr error) {
	t.Helper()
	replay, err := fn()
	if replay != wantReplay || err != wantErr {
		t.Fatalf("result = (%t, %v); want (%t, %v)", replay, err, wantReplay, wantErr)
	}
}

func balance(t testing.TB, j *Journal, want Balance) {
	t.Helper()
	if got := j.Balance(); got != want {
		t.Fatalf("balance %+v; want %+v", got, want)
	}
}

func TestSplitTenderAndSeal(t *testing.T) {
	j := journal(t, 1599)
	cash := Leg{request(3, 1), "efectivo", 500}
	card := Leg{request(3, 2), "debito", 599}
	anotherClerk := Leg{request(4, 3), "efectivo", 500}
	seal := request(4, 4)
	result(t, func() (bool, error) { return j.Record(cash) }, false, nil)
	result(t, func() (bool, error) { return j.Record(card) }, false, nil)
	result(t, func() (bool, error) { return j.Seal(seal) }, false, ErrUnpaid)
	balance(t, j, Balance{1599, 1099, 500, 2, false})
	result(t, func() (bool, error) { return j.Record(anotherClerk) }, false, nil)
	balance(t, j, Balance{1599, 1599, 0, 3, false})
	result(t, func() (bool, error) { return j.Seal(seal) }, false, nil)
	for range 2 {
		result(t, func() (bool, error) { return j.Record(card) }, true, nil)
		result(t, func() (bool, error) { return j.Seal(seal) }, true, nil)
	}
	result(t, func() (bool, error) { return j.Record(Leg{request(5, 5), "efectivo", 1}) }, false, ErrSealed)
	result(t, func() (bool, error) { return j.Seal(request(5, 5)) }, false, ErrSealed)
	balance(t, j, Balance{1599, 1599, 0, 3, true})
	legs := j.Legs()
	legs[0] = Leg{}
	if j.Legs()[0] != cash || j.Legs()[1] != card || j.Legs()[2] != anotherClerk {
		t.Fatal("leg order or immutability changed")
	}
}

func TestDuplicateAndScopeDenyWithoutAffectingBalance(t *testing.T) {
	j := journal(t, 200)
	r := request(3, 1)
	leg := Leg{r, "efectivo", 100}
	result(t, func() (bool, error) { return j.Record(leg) }, false, nil)
	result(t, func() (bool, error) { return j.Record(leg) }, true, nil)
	for name, changed := range map[string]Leg{
		"amount": {r, "efectivo", 99},
		"method": {r, "credito", 100},
		"actor":  {request(4, 1), "efectivo", 100},
	} {
		t.Run(name, func(t *testing.T) {
			result(t, func() (bool, error) { return j.Record(changed) }, false, ErrReplay)
		})
	}
	for _, wrong := range []Scope{{12, 21, 31}, {11, 22, 31}, {11, 21, 32}} {
		changed := leg
		changed.Request.Scope = wrong
		result(t, func() (bool, error) { return j.Record(changed) }, false, ErrScope)
		result(t, func() (bool, error) { return j.Seal(changed.Request) }, false, ErrScope)
	}
	result(t, func() (bool, error) { return j.Seal(r) }, false, ErrReplay)
	balance(t, j, Balance{200, 100, 100, 1, false})
	result(t, func() (bool, error) { return j.Record(Leg{request(3, 2), "qr_modo", 100}) }, false, nil)
	result(t, func() (bool, error) { return j.Seal(request(3, 3)) }, false, nil)
	result(t, func() (bool, error) { return j.Seal(request(4, 3)) }, false, ErrReplay)
	result(t, func() (bool, error) { return j.Record(Leg{request(3, 3), "efectivo", 1}) }, false, ErrReplay)
	balance(t, j, Balance{200, 200, 0, 2, true})
}

func TestLimitsValidationAndRemainingExact(t *testing.T) {
	for _, amount := range []int64{math.MinInt64, -1, 0, MaxTotalMinor + 1, math.MaxInt64} {
		if _, err := New(check, amount); err != ErrInvalid {
			t.Errorf("invalid check total accepted: %d", amount)
		}
	}
	if _, err := New(Scope{EntityID: 1, OutletID: 1}, 1); err != ErrInvalid {
		t.Fatalf("invalid scope result: %v", err)
	}
	j := journal(t, MaxTotalMinor)
	r := request(3, 7)
	wrongID := r
	wrongID.RequestID = "not a UUID"
	anon := r
	anon.ActorID = 0
	for _, leg := range []Leg{
		{r, "", 1}, {r, "qr", 1}, {r, "DEBITO", 1}, {r, "efectivo ", 1}, {r, "efectivo", 0},
		{r, "efectivo", math.MinInt64}, {wrongID, "efectivo", 1}, {anon, "efectivo", 1},
	} {
		result(t, func() (bool, error) { return j.Record(leg) }, false, ErrInvalid)
	}
	result(t, func() (bool, error) { return j.Record(Leg{r, "qr_mercado_pago", MaxTotalMinor - 1}) }, false, nil)
	result(t, func() (bool, error) { return j.Record(Leg{request(3, 8), "credito", math.MaxInt64}) }, false, ErrOverpay)
	balance(t, j, Balance{MaxTotalMinor, MaxTotalMinor - 1, 1, 1, false})
	result(t, func() (bool, error) { return j.Record(Leg{request(3, 8), "transferencia", 1}) }, false, nil)
	result(t, func() (bool, error) { return j.Record(Leg{request(3, 9), "credito", 1}) }, false, ErrOverpay)
	balance(t, j, Balance{MaxTotalMinor, MaxTotalMinor, 0, 2, false})
}

func TestLastLegCannotStrandAResidual(t *testing.T) {
	j := journal(t, MaxLegs+1)
	for i := range MaxLegs - 1 {
		result(t, func() (bool, error) { return j.Record(Leg{request(3, uint64(i+1)), "efectivo", 1}) }, false, nil)
	}
	last := request(3, MaxLegs)
	result(t, func() (bool, error) { return j.Record(Leg{last, "efectivo", 1}) }, false, ErrTooMany)
	result(t, func() (bool, error) { return j.Record(Leg{last, "credito", 2}) }, false, nil)
	balance(t, j, Balance{MaxLegs + 1, MaxLegs + 1, 0, MaxLegs, false})
	result(t, func() (bool, error) { return j.Seal(request(3, MaxLegs+1)) }, false, nil)
}

func FuzzTenderNeverOverpays(f *testing.F) {
	f.Add(int64(500), int64(400), "debito")
	f.Add(int64(1000), int64(1), "efectivo")
	f.Add(int64(-1), int64(math.MaxInt64), "credito")
	f.Fuzz(func(t *testing.T, first, second int64, method string) {
		j := journal(t, 1000)
		for i, amount := range []int64{first, second} {
			before := j.Balance()
			leg := Leg{request(1, uint64(i+1)), method, amount}
			replay, err := j.Record(leg)
			after := j.Balance()
			if replay || (err == nil) != (IsManualMethod(method) && amount > 0 && amount <= before.OutstandingMinor) {
				t.Fatalf("invalid result: %t %v for %+v and %+v", replay, err, before, leg)
			}
			if err != nil {
				balance(t, j, before)
			} else if after.RecordedMinor != before.RecordedMinor+amount ||
				after.OutstandingMinor < 0 || after.OutstandingMinor+after.RecordedMinor != after.TotalMinor ||
				after.LegCount != before.LegCount+1 || after.Sealed {
				t.Fatalf("invalid change %+v to %+v", before, after)
			}
		}
	})
}
