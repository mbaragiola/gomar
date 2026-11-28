package main

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const employeeAuthWindow = 5 * time.Minute
const employeeAuthAttempts = 8
const employeeAuthEntries = 4096
const employeeAuthWorkers = 2

type employeeAuthAttempt struct {
	ends  time.Time
	count int
}
type employeeAuthBudget struct {
	sync.Mutex
	attempts map[[32]byte]employeeAuthAttempt
	inFlight map[int64]bool
}

func (b *employeeAuthBudget) enter(organization int64, key [32]byte, now time.Time) bool {
	b.Lock()
	defer b.Unlock()
	if len(b.inFlight) >= employeeAuthWorkers || b.inFlight[organization] {
		return false
	}
	// Memory and concurrent expensive password work both have hard bounds.
	// Unknown and known account coordinates use the same budget.
	if b.attempts == nil {
		b.attempts = make(map[[32]byte]employeeAuthAttempt)
	}
	a, found := b.attempts[key]
	if found && !now.Before(a.ends) {
		delete(b.attempts, key)
		found = false
	}
	if !found {
		if len(b.attempts) >= employeeAuthEntries {
			for k, old := range b.attempts {
				if !now.Before(old.ends) {
					delete(b.attempts, k)
				}
			}
			if len(b.attempts) >= employeeAuthEntries {
				return false
			}
		}
		a = employeeAuthAttempt{ends: now.Add(employeeAuthWindow)}
	}
	if a.count >= employeeAuthAttempts {
		return false
	}
	a.count++
	b.attempts[key] = a
	if b.inFlight == nil {
		b.inFlight = make(map[int64]bool)
	}
	b.inFlight[organization] = true
	return true
}
func (b *employeeAuthBudget) leave(organization int64) {
	b.Lock()
	delete(b.inFlight, organization)
	b.Unlock()
}

func employeeLoginBudgetKey(org int64, username string) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("login/%d/%s", org, username)))
}

const signInRequestTimeout = 15 * time.Second

type signInWork struct{ inFlight atomic.Int32 }

func (w *signInWork) enter(limit int32) bool {
	for {
		current := w.inFlight.Load()
		if current >= limit {
			return false
		}
		if w.inFlight.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (w *signInWork) leave() { w.inFlight.Add(-1) }

var employeeAuthBusy = problem{429, "Hay demasiados intentos. Esperá unos minutos y volvé a intentar."}
