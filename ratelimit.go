package main

import (
	"sync"
	"time"
)

const (
	maxFailedAttempts = 5
	lockoutDuration   = 15 * time.Minute
)

type attemptRecord struct {
	count       int
	lockedUntil time.Time
}

var (
	loginAttempts   = make(map[string]*attemptRecord)
	loginAttemptsMu sync.Mutex
)

func isLockedOut(username string) (bool, time.Duration) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	rec, ok := loginAttempts[username]
	if !ok || time.Now().After(rec.lockedUntil) {
		return false, 0
	}
	return true, time.Until(rec.lockedUntil).Round(time.Minute)
}

func recordFailedLogin(username string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	rec, ok := loginAttempts[username]
	if !ok {
		rec = &attemptRecord{}
		loginAttempts[username] = rec
	}

	rec.count++
	if rec.count >= maxFailedAttempts {
		rec.lockedUntil = time.Now().Add(lockoutDuration)
		rec.count = 0
	}
}

func clearLoginAttempts(username string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	delete(loginAttempts, username)
}
