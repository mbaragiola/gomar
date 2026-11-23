package main

import (
	"testing"
	"time"
)

func TestEmployeeAuthBudget(t *testing.T) {
	var b employeeAuthBudget
	now := time.Unix(10, 0)
	a := employeeLoginBudgetKey(1, "example")
	other := employeeLoginBudgetKey(2, "example")
	third := employeeLoginBudgetKey(1, "another")
	if !b.enter(a, now) || !b.enter(other, now) || b.enter(third, now) {
		t.Fatal("concurrent password work exceeded the bound")
	}
	b.leave()
	b.leave()
	for i := 1; i < employeeAuthAttempts; i++ {
		if !b.enter(a, now) {
			t.Fatal("refused an attempt before the limit")
		}
		b.leave()
	}
	if b.enter(a, now.Add(employeeAuthWindow-time.Millisecond)) {
		t.Fatal("allowance returned too early")
	}
	if !b.enter(other, now) {
		t.Fatal("organization coordinates were not independent")
	}
	b.leave()
	if !b.enter(a, now.Add(employeeAuthWindow)) {
		t.Fatal("did not restore allowance after the window")
	}
	b.leave()
}
