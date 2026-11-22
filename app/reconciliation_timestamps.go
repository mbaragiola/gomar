package main

import "time"

func (a *app) reconciliationRowTime() string {
	clock := a.reconciliationClock
	if clock == nil {
		clock = time.Now
	}
	return clock().UTC().Format(microsecondsUTC)
}
