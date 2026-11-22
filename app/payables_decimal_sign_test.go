package main

import "testing"

func TestPayableMoneyPlainASCIISigns(t *testing.T) {
	for _, tc := range []struct {
		input string
		minor int64
	}{
		{"41.81", 4181}, {"+41,81", 4181}, {"+41.81", 4181}, {"  +0,5  ", 50},
		{"+.50", 50}, {"+1.", 100}, {"0", 0}, {"-0,00", 0}, {"+0.00", 0},
		{"-0", 0}, {"-.00", 0}, {"+999999999999,99", maxPayableMinor},
	} {
		if got, err := parsePayableMoney(tc.input, "Total"); err != nil || got != tc.minor {
			t.Errorf("%q = %d, %v; want %d minor units", tc.input, got, err, tc.minor)
		}
	}
	for _, input := range []string{
		"-1,00", "-0.01", "-0,001", "+1.234", "+1,2.3",
		"+", "-", "+.", "-,", "++1", "--0", "+-0", "-+0", "+ 1", "1+",
		"＋1", "−0", "+1e0", "+1_0", "+NaN", "+Inf",
		"+1000000000000.00", "+9223372036854775808.00",
	} {
		for _, label := range []string{"Total", "Importe"} {
			want := problem{422, label + ": ingresá un importe válido."}
			if got, err := parsePayableMoney(input, label); got != 0 || err != want {
				t.Errorf("%q (%s) = %d, %v; want %v", input, label, got, err, want)
			}
		}
	}
}
