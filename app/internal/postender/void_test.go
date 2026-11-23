package postender

import "testing"

func TestVoidRecordAndExplicitSeal(t *testing.T) {
	j := journal(t, 200)
	cash := Leg{request(3, 1), "efectivo", 99}
	card := Leg{request(3, 2), "credito", 101}
	correction := Void{request(4, 3), cash.Request.RequestID, VoidAmountError}
	result(t, func() (bool, error) { return j.Record(cash) }, false, nil)
	result(t, func() (bool, error) { return j.Record(card) }, false, nil)
	result(t, func() (bool, error) { return j.Void(correction) }, false, nil)
	balance(t, j, Balance{200, 101, 99, 1, false})
	result(t, func() (bool, error) { return j.Seal(request(4, 4)) }, false, ErrUnpaid)
	result(t, func() (bool, error) { return j.Record(cash) }, true, nil)
	result(t, func() (bool, error) { return j.Void(correction) }, true, nil)
	balance(t, j, Balance{200, 101, 99, 1, false})

	replacement := Leg{request(4, 5), "qr_mercado_pago", 99}
	result(t, func() (bool, error) { return j.Record(replacement) }, false, nil)
	result(t, func() (bool, error) { return j.Seal(request(4, 4)) }, false, nil)
	result(t, func() (bool, error) { return j.Void(correction) }, true, nil)
	result(t, func() (bool, error) {
		return j.Void(Void{request(4, 6), card.Request.RequestID, VoidMethodError})
	}, false, ErrSealed)
	balance(t, j, Balance{200, 200, 0, 2, true})
	history := j.Voids()
	if len(history) != 1 || history[0] != correction || j.Legs()[0] != cash || j.Legs()[2] != replacement {
		t.Fatal("tender audit history changed or a correction erased the original")
	}
	history[0] = Void{}
	if j.Voids()[0] != correction {
		t.Fatal("audit output mutated the journal")
	}
}

func TestVoidScopeAndReplay(t *testing.T) {
	j := journal(t, 300)
	first := Leg{request(3, 1), "efectivo", 100}
	second := Leg{request(3, 2), "debito", 99}
	result(t, func() (bool, error) { return j.Record(first) }, false, nil)
	result(t, func() (bool, error) { return j.Record(second) }, false, nil)
	v := Void{request(4, 3), first.Request.RequestID, VoidEntryError}
	for _, invalid := range []Void{
		{v.Request, "not UUID", v.Reason},
		{v.Request, v.Request.RequestID, v.Reason},
		{v.Request, first.Request.RequestID, "no free text"},
	} {
		result(t, func() (bool, error) { return j.Void(invalid) }, false, ErrInvalid)
	}
	for _, wrongScope := range []Scope{{12, 21, 31}, {11, 22, 31}, {11, 21, 32}} {
		foreign := v
		foreign.Request.Scope = wrongScope
		result(t, func() (bool, error) { return j.Void(foreign) }, false, ErrScope)
	}
	result(t, func() (bool, error) {
		return j.Void(Void{request(4, 3), request(4, 4).RequestID, v.Reason})
	}, false, ErrMissingLeg)
	result(t, func() (bool, error) { return j.Void(v) }, false, nil)
	for _, changed := range []Void{
		{request(5, 3), v.TargetRequestID, v.Reason},
		{v.Request, second.Request.RequestID, v.Reason},
		{v.Request, v.TargetRequestID, VoidAmountError},
	} {
		result(t, func() (bool, error) { return j.Void(changed) }, false, ErrReplay)
	}
	result(t, func() (bool, error) { return j.Void(Void{request(5, 4), v.TargetRequestID, v.Reason}) }, false, ErrVoided)
	result(t, func() (bool, error) { return j.Record(Leg{v.Request, "efectivo", 1}) }, false, ErrReplay)
	result(t, func() (bool, error) { return j.Seal(v.Request) }, false, ErrReplay)
	result(t, func() (bool, error) { return j.Void(Void{first.Request, second.Request.RequestID, v.Reason}) }, false, ErrReplay)
	balance(t, j, Balance{300, 99, 201, 1, false})
}

func TestVoidFreesOneActiveSlot(t *testing.T) {
	j := journal(t, MaxLegs)
	for i := range MaxLegs {
		result(t, func() (bool, error) { return j.Record(Leg{request(3, uint64(i+1)), "efectivo", 1}) }, false, nil)
	}
	v := Void{request(3, MaxLegs+1), request(3, 1).RequestID, VoidEntryError}
	result(t, func() (bool, error) { return j.Void(v) }, false, nil)
	balance(t, j, Balance{MaxLegs, MaxLegs - 1, 1, MaxLegs - 1, false})
	result(t, func() (bool, error) { return j.Record(Leg{request(3, MaxLegs+2), "efectivo", 1}) }, false, nil)
	if len(j.Legs()) != MaxLegs+1 {
		t.Fatal("original record removed from chronological history")
	}
	balance(t, j, Balance{MaxLegs, MaxLegs, 0, MaxLegs, false})
}

func FuzzVoidReplaysDoNotRestoreTender(f *testing.F) {
	f.Add(int64(123), int64(567), "credito")
	f.Add(int64(999), int64(1), "debito")
	f.Fuzz(func(t *testing.T, first, second int64, method string) {
		j := journal(t, 1000)
		original := Leg{request(3, 1), method, first}
		other := Leg{request(3, 2), "efectivo", second}
		if _, err := j.Record(original); err != nil {
			t.Skip()
		}
		_, _ = j.Record(other)
		before := j.Balance()
		v := Void{request(4, 3), original.Request.RequestID, VoidAmountError}
		result(t, func() (bool, error) { return j.Void(v) }, false, nil)
		want := Balance{1000, before.RecordedMinor - first, before.OutstandingMinor + first, before.LegCount - 1, false}
		balance(t, j, want)
		result(t, func() (bool, error) { return j.Record(original) }, true, nil)
		result(t, func() (bool, error) { return j.Void(v) }, true, nil)
		balance(t, j, want)
	})
}
