package main

import (
	"testing"
	"time"
)

func TestFormatPayableStorageTime(t *testing.T) {
	for _, tc := range []struct {
		name, input, stored, wire string
	}{
		{"six digits", "2028-01-03T05:06:07.123456Z", "2028-01-03T05:06:07.123456Z", "2028-01-03T05:06:07.123456+00:00"},
		{"trailing zeros", "2028-01-03T05:06:07.1234Z", "2028-01-03T05:06:07.123400Z", "2028-01-03T05:06:07.123400+00:00"},
		{"same instant at offset", "2028-01-03T02:06:07.1234-03:00", "2028-01-03T05:06:07.123400Z", "2028-01-03T05:06:07.123400+00:00"},
		{"truncate without rounding", "2028-01-03T05:06:07.999999999Z", "2028-01-03T05:06:07.999999Z", "2028-01-03T05:06:07.999999+00:00"},
		{"one microsecond", "2028-01-03T05:06:07.000001Z", "2028-01-03T05:06:07.000001Z", "2028-01-03T05:06:07.000001+00:00"},
		{"sub-microsecond down to zero", "2028-01-03T05:06:07.000000999Z", "2028-01-03T05:06:07Z", "2028-01-03T05:06:07+00:00"},
		{"whole second", "2028-01-03T05:06:07Z", "2028-01-03T05:06:07Z", "2028-01-03T05:06:07+00:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339Nano, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			got := formatPayableStorageTime(at)
			if got != tc.stored {
				t.Fatalf("stored timestamp = %q, want %q", got, tc.stored)
			}
			if wire := payableAPITime(got); wire != tc.wire {
				t.Fatalf("wire timestamp = %q, want %q", wire, tc.wire)
			}
			if wire := payableAPITime(tc.wire); wire != tc.wire {
				t.Fatalf("old imported wire timestamp = %q, want %q", wire, tc.wire)
			}
		})
	}
}

func TestPayableClockKeepsInjectedMicroseconds(t *testing.T) {
	at := time.Date(2028, time.January, 3, 2, 6, 7, 123400999, time.FixedZone("UTC-3", -3*60*60))
	a := app{payablesClock: func() time.Time { return at }}
	got := formatPayableStorageTime(a.payableNow())
	if got != "2028-01-03T05:06:07.123400Z" {
		t.Fatalf("stored injected clock = %q", got)
	}
	if wire := payableAPITime(got); wire != "2028-01-03T05:06:07.123400+00:00" {
		t.Fatalf("wire injected clock = %q", wire)
	}
}
