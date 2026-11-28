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
	if !b.enter(1, a, now) || b.enter(1, third, now) ||
		!b.enter(2, other, now) || b.enter(3, employeeLoginBudgetKey(3, "third"), now) {
		t.Fatal("concurrent password work exceeded the bound")
	}
	b.leave(1)
	b.leave(2)
	for i := 1; i < employeeAuthAttempts; i++ {
		if !b.enter(1, a, now) {
			t.Fatal("refused an attempt before the limit")
		}
		b.leave(1)
	}
	if b.enter(1, a, now.Add(employeeAuthWindow-time.Millisecond)) {
		t.Fatal("allowance returned too early")
	}
	if !b.enter(2, other, now) {
		t.Fatal("organization coordinates were not independent")
	}
	b.leave(2)
	if !b.enter(1, a, now.Add(employeeAuthWindow)) {
		t.Fatal("did not restore allowance after the window")
	}
	b.leave(1)
}
