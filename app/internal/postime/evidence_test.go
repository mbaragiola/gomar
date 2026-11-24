package postime

import (
	"testing"
	"time"
)

func TestParseTillUTC(t *testing.T) {
	for _, valid := range []string{"2026-12-01T02:58:00Z", "2026-12-01T02:58:00.001Z", "2028-02-29T23:59:59.999999999Z"} {
		if _, ok := ParseUTC(valid); !ok {
			t.Errorf("rejected %q", valid)
		}
	}
	for _, invalid := range []string{
		"", "2026-12-01T02:58:00z", "2026-12-01t02:58:00Z", "2026-12-01T02:58:00",
		"2026-12-01T02:58:00+00:00", "2026-11-30T23:58:00-03:00", "2026-12-01T02:58:00,5Z",
		"2026-12-01T2:58:00Z", "2026-12-01T24:00:00Z", "2026-12-01T02:58:60Z",
		"2026-12-01T02:58:00.1234567890Z", "2026-02-29T02:58:00Z", "0000-01-01T02:58:00Z",
		" 2026-12-01T02:58:00Z", "2026-12-01T02:58:00Z\n",
	} {
		if _, ok := ParseUTC(invalid); ok {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestStatusUsesRegistrationClockWithoutClaimingVerification(t *testing.T) {
	registered := time.Date(2026, 12, 1, 3, 7, 0, 0, time.UTC)
	for _, tc := range []struct{ raw, want string }{
		{"", NotReported},
		{"2026-12-01T02:58:00Z", Unverified},
		{"2026-12-01T03:12:00Z", Unverified},
		{"2026-12-01T03:12:00.000000001Z", Review},
		{"2026-11-30T03:07:00Z", Unverified},
		{"2026-11-30T03:06:59.999999999Z", Review},
		{"2004-02-29T03:07:00Z", Review},
	} {
		if got, ok := Status(tc.raw, registered); !ok || got != tc.want {
			t.Errorf("%q -> %q, %t; want %q", tc.raw, got, ok, tc.want)
		}
	}
	if _, ok := Status("not a timestamp", registered); ok {
		t.Fatal("classified invalid evidence")
	}
}
