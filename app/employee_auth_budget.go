package main

import (
	"crypto/sha256"
	"fmt"
	"sync"
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
	inFlight int
}

func (b *employeeAuthBudget) enter(key [32]byte, now time.Time) bool {
	b.Lock()
	defer b.Unlock()
	if b.inFlight >= employeeAuthWorkers {
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
	b.inFlight++
	return true
}
func (b *employeeAuthBudget) leave() {
	b.Lock()
	b.inFlight--
	b.Unlock()
}

func employeeLoginBudgetKey(org int64, username string) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("login/%d/%s", org, username)))
}

var employeeAuthBusy = problem{429, "Hay demasiados intentos. Esperá unos minutos y volvé a intentar."}
