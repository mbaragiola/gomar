package tenantfiscalpreview

import (
	"errors"
	"testing"
)

func fixture() (Pilot, Operator, Sale) {
	outlet := Scope{EntityID: 1, OutletID: 9}
	return Pilot{Enabled: true, Scope: outlet, Issuer: "fixture-mesa", PointOfSale: 8},
		Operator{Scope: outlet, ActorID: 14, MayIssue: true},
		Sale{Scope: outlet, ID: 25, TotalMinor: 321_00, OperationDate: "2026-11-23", OnlineRecorded: true, Due: true}
}

func buildAttempt(t *testing.T, number int) Attempt {
	t.Helper()
	p, o, s := fixture()
	draft, err := Prepare(p, o, s)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := Confirm(draft, p, o, s)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := confirmed.WithReservedNumber(number)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func TestPrepareRejectsUntrustedScopeAndUnsupportedSales(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Pilot, *Operator, *Sale)
		want   error
	}{
		{"disabled", func(p *Pilot, _ *Operator, _ *Sale) { p.Enabled = false }, ErrUnavailable},
		{"permission", func(_ *Pilot, o *Operator, _ *Sale) { o.MayIssue = false }, ErrUnavailable},
		{"no actor", func(_ *Pilot, o *Operator, _ *Sale) { o.ActorID = 0 }, ErrUnavailable},
		{"another entity with same outlet", func(_ *Pilot, o *Operator, _ *Sale) { o.Scope.EntityID++ }, ErrUnavailable},
		{"sibling outlet", func(_ *Pilot, o *Operator, _ *Sale) { o.Scope.OutletID++ }, ErrUnavailable},
		{"foreign sale", func(_ *Pilot, _ *Operator, s *Sale) { s.Scope.EntityID++ }, ErrUnavailable},
		{"sale in wrong outlet", func(_ *Pilot, _ *Operator, s *Sale) { s.Scope.OutletID++ }, ErrUnavailable},
		{"numeric issuer", func(p *Pilot, _ *Operator, _ *Sale) { p.Issuer = "00000000000" }, ErrUnavailable},
		{"point too large", func(p *Pilot, _ *Operator, _ *Sale) { p.PointOfSale = 100000 }, ErrUnavailable},
		{"offline sale", func(_ *Pilot, _ *Operator, s *Sale) { s.OnlineRecorded = false }, ErrSale},
		{"operation not fiscal due", func(_ *Pilot, _ *Operator, s *Sale) { s.Due = false }, ErrSale},
		{"invalid date", func(_ *Pilot, _ *Operator, s *Sale) { s.OperationDate = "2026-02-30" }, ErrSale},
		{"raw sync timestamp", func(_ *Pilot, _ *Operator, s *Sale) { s.OperationDate = "2026-11-23T15:00:00Z" }, ErrSale},
		{"no stable sale", func(_ *Pilot, _ *Operator, s *Sale) { s.ID = 0 }, ErrSale},
		{"zero total", func(_ *Pilot, _ *Operator, s *Sale) { s.TotalMinor = 0 }, ErrSale},
		{"fixture ceiling exceeded", func(_ *Pilot, _ *Operator, s *Sale) { s.TotalMinor = maxFixtureTotal + 1 }, ErrSale},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, o, s := fixture()
			tc.change(&p, &o, &s)
			got, err := Prepare(p, o, s)
			if !errors.Is(err, tc.want) || got != (Preview{}) {
				t.Fatalf("got (%v,%v), want empty and %v", got, err, tc.want)
			}
		})
	}
}

func TestConfirmRechecksExactNativeReview(t *testing.T) {
	p, o, s := fixture()
	reviewed, err := Prepare(p, o, s)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*Pilot, *Operator, *Sale)
		want   error
	}{
		{"another authorized actor", func(_ *Pilot, o *Operator, _ *Sale) { o.ActorID++ }, ErrChanged},
		{"changed sale", func(_ *Pilot, _ *Operator, s *Sale) { s.ID++ }, ErrChanged},
		{"changed total", func(_ *Pilot, _ *Operator, s *Sale) { s.TotalMinor++ }, ErrChanged},
		{"changed operation date", func(_ *Pilot, _ *Operator, s *Sale) { s.OperationDate = "2026-11-24" }, ErrChanged},
		{"changed issuer", func(p *Pilot, _ *Operator, _ *Sale) { p.Issuer = "fixture-other" }, ErrChanged},
		{"changed point", func(p *Pilot, _ *Operator, _ *Sale) { p.PointOfSale++ }, ErrChanged},
		{"revoked authorization", func(_ *Pilot, o *Operator, _ *Sale) { o.MayIssue = false }, ErrUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, o, s := fixture()
			tc.change(&p, &o, &s)
			got, err := Confirm(reviewed, p, o, s)
			if !errors.Is(err, tc.want) || got != (Confirmed{}) {
				t.Fatalf("got (%v,%v), want empty and %v", got, err, tc.want)
			}
		})
	}
	if _, err := Confirm(Preview{}, p, o, s); !errors.Is(err, ErrChanged) {
		t.Fatalf("zero preview got %v", err)
	}
	if _, err := (Confirmed{}).WithReservedNumber(5); !errors.Is(err, ErrAttempt) {
		t.Fatalf("zero confirmation got %v", err)
	}
}

func TestReservedNumberIsNotFromTenderOrSaleID(t *testing.T) {
	p, o, s := fixture()
	preview, err := Prepare(p, o, s)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Confirm(preview, p, o, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{-1, 0, 100000000} {
		if _, err := c.WithReservedNumber(n); !errors.Is(err, ErrAttempt) {
			t.Fatalf("number %d got %v", n, err)
		}
	}
	a, err := c.WithReservedNumber(901)
	if err != nil {
		t.Fatal(err)
	}
	want := Payload{Key: Key{Issuer: "fixture-mesa", PointOfSale: 8, DocumentType: 11, Number: 901},
		IssueDate: "2026-11-23", Currency: "PES", RecipientKind: 5, TotalMinor: 321_00}
	if a.Payload() != want {
		t.Fatalf("got %#v, want %#v", a.Payload(), want)
	}
}

func assertStatus(t *testing.T, d Decision, err error, want Status) {
	t.Helper()
	if err != nil || d.Status() != want {
		t.Fatalf("got (%v,%v), want %v", d.Status(), err, want)
	}
	code, expires, ok := d.Authorization()
	if want == Authorized {
		if !ok || code != "10000000000001" || expires != "2026-12-03" {
			t.Fatalf("wrong authorization %q %q %v", code, expires, ok)
		}
	} else if ok || code != "" || expires != "" {
		t.Fatal("non-authorized decision exposed an authorization")
	}
}

func good(a Attempt) Reply {
	return Reply{Outcome: Approved, Echo: a.Payload(), Code: "10000000000001", ExpiresOn: "2026-12-03"}
}

func TestSubmitHandlesExactApprovalAndDefiniteRejection(t *testing.T) {
	a := buildAttempt(t, 33)
	valid := good(a)
	refused := Reply{Outcome: Rejected, Echo: a.Payload()}
	for _, tc := range []struct {
		name string
		r    Reply
		want Status
	}{
		{"approval", valid, Authorized},
		{"definite rejection", refused, Refused},
		{"rejection without request identity", Reply{Outcome: Rejected}, Unknown},
		{"provider offline", Reply{Outcome: Unavailable}, Unknown},
		{"missing reply", Reply{}, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := AfterSubmit(a, tc.r)
			assertStatus(t, d, err, tc.want)
		})
	}
	if _, err := AfterSubmit(Attempt{}, valid); !errors.Is(err, ErrAttempt) {
		t.Fatalf("zero attempt got %v", err)
	}
}

type fakeWSFE struct {
	calls   int
	records map[Key]Reply
}

func (f *fakeWSFE) submitButLoseResponse(a Attempt) Reply {
	f.calls++
	f.records[a.Payload().Key] = good(a)
	return Reply{Outcome: Unavailable}
}
func (f *fakeWSFE) lookup(k Key) Reply {
	r, found := f.records[k]
	if !found {
		return Reply{Outcome: NotFound}
	}
	return r
}

func TestRecoveredAuthorizationDoesNotNeedSecondSubmit(t *testing.T) {
	a := buildAttempt(t, 102)
	f := fakeWSFE{records: make(map[Key]Reply)}
	unknown, err := AfterSubmit(a, f.submitButLoseResponse(a))
	assertStatus(t, unknown, err, Unknown)
	stillUnknown, err := AfterLookup(unknown, a, Reply{Outcome: NotFound})
	assertStatus(t, stillUnknown, err, Unknown)
	if stillUnknown != unknown {
		t.Fatal("not found changed the attempt")
	}
	resolved, err := AfterLookup(stillUnknown, a, f.lookup(a.Payload().Key))
	assertStatus(t, resolved, err, Authorized)
	if f.calls != 1 {
		t.Fatalf("got %d dispatches, want one", f.calls)
	}
	if _, err := AfterLookup(resolved, a, Reply{Outcome: Unavailable}); !errors.Is(err, ErrAttempt) {
		t.Fatalf("an authorized decision must not regress: %v", err)
	}
	if _, err := AfterLookup(unknown, buildAttempt(t, 103), good(a)); !errors.Is(err, ErrAttempt) {
		t.Fatalf("a different attempt must not resolve the uncertain one: %v", err)
	}
}

func TestMismatchedOrMalformedApprovalFailsClosed(t *testing.T) {
	a := buildAttempt(t, 42)
	for _, tc := range []struct {
		name   string
		change func(*Reply)
	}{
		{"wrong issuer", func(r *Reply) { r.Echo.Key.Issuer = "fixture-other" }},
		{"wrong point", func(r *Reply) { r.Echo.Key.PointOfSale++ }},
		{"wrong document type", func(r *Reply) { r.Echo.Key.DocumentType++ }},
		{"wrong number", func(r *Reply) { r.Echo.Key.Number++ }},
		{"wrong total", func(r *Reply) { r.Echo.TotalMinor++ }},
		{"missing currency", func(r *Reply) { r.Echo.Currency = "" }},
		{"missing recipient type", func(r *Reply) { r.Echo.RecipientKind = 0 }},
		{"different date", func(r *Reply) { r.Echo.IssueDate = "2026-11-24" }},
		{"missing code", func(r *Reply) { r.Code = "" }},
		{"placeholder code", func(r *Reply) { r.Code = "00000000000000" }},
		{"expired before issuance", func(r *Reply) { r.ExpiresOn = "2026-11-22" }},
		{"invalid expiry date", func(r *Reply) { r.ExpiresOn = "2026-11-31" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := good(a)
			tc.change(&r)
			d, err := AfterSubmit(a, r)
			assertStatus(t, d, err, Unknown)
			d, err = AfterLookup(d, a, r)
			assertStatus(t, d, err, NeedsReview)
		})
	}
}
