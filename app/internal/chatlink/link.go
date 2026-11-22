// Package chatlink models a disposable Gomar link for reading payable totals.
// It is not a transport, an identity provider, or a production grant store.
package chatlink

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

type Channel string

const (
	CLI      Channel = "cli"
	WhatsApp Channel = "whatsapp"
)

const (
	pendingLife = 5 * time.Minute
	linkedLife  = 8 * time.Hour
	maxEntries  = 256
	proofBytes  = 32
	proofLength = 43 // Raw URL base64 width for 32 bytes.
)

var (
	// Denied covers ordinary unlinked, invalid, expired, or disallowed requests.
	Denied = errors.New("chat link unavailable")
	// Capacity distinguishes temporary exhaustion from an ordinary denial.
	Capacity = errors.New("chat link capacity reached")
	// Unavailable identifies internal failures without leaking policy details.
	Unavailable = errors.New("chat link check failed")
)

// Subject is a channel identity already verified by the trusted caller.
// Proof possession does not establish this identity. Do not populate it from chat text.
type Subject struct {
	Channel   Channel
	Principal string
}

// Scope is the explicit Gomar selection. Payable totals are the only action.
type Scope struct {
	ActorID  int64
	EntityID int64
	OutletID int64
}

// Policy must evaluate the current permission for the supplied identity and scope.
// A real app read must still enforce its own policy in the same read transaction.
type Policy interface {
	CanReadPayableTotals(context.Context, Subject, Scope) (bool, error)
}

type entry struct {
	scope   Scope
	digest  [sha256.Size]byte // Zero after pairing; plaintext is never retained.
	expires time.Time
	linked  bool
}

type Store struct {
	mu     sync.Mutex
	now    func() time.Time
	policy Policy
	items  map[Subject]*entry
}

// New creates an empty, isolated store. There is no global state.
func New(now func() time.Time, policy Policy) (*Store, error) {
	if now == nil || policy == nil {
		return nil, Unavailable
	}
	return &Store{now: now, policy: policy, items: make(map[Subject]*entry)}, nil
}

func validSubject(subject Subject) bool {
	return (subject.Channel == CLI || subject.Channel == WhatsApp) &&
		len(subject.Principal) > 0 && len(subject.Principal) <= 128
}

func (s *Store) allowed(ctx context.Context, subject Subject, scope Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	allowed, err := s.policy.CanReadPayableTotals(ctx, subject, scope)
	if err != nil {
		return Unavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !allowed {
		return Denied
	}
	return nil
}

// Begin is for a trusted caller recording an explicit approved scope. It
// returns a one-time proof to that caller; it must not be logged or shared.
func (s *Store) Begin(ctx context.Context, subject Subject, scope Scope) (string, error) {
	if !validSubject(subject) || scope.ActorID <= 0 || scope.EntityID <= 0 || scope.OutletID <= 0 {
		return "", Denied
	}
	if err := s.allowed(ctx, subject, scope); err != nil {
		return "", err
	}
	var raw [proofBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", Unavailable
	}
	proof := base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(proof))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	now := s.now()
	for key, item := range s.items {
		if !now.Before(item.expires) {
			delete(s.items, key)
		}
	}
	if _, present := s.items[subject]; present {
		return "", Denied
	}
	if len(s.items) >= maxEntries {
		return "", Capacity
	}
	s.items[subject] = &entry{scope: scope, digest: digest, expires: now.Add(pendingLife)}
	return proof, nil
}

// Complete consumes the proof once for the same verified channel and
// principal. It does not allow a client to choose a scope at completion.
func (s *Store) Complete(ctx context.Context, subject Subject, proof string) error {
	if !validSubject(subject) || len(proof) != proofLength {
		return Denied
	}
	digest := sha256.Sum256([]byte(proof))
	s.mu.Lock()
	item := s.items[subject]
	if item == nil || item.linked || !s.now().Before(item.expires) ||
		subtle.ConstantTimeCompare(digest[:], item.digest[:]) != 1 {
		s.mu.Unlock()
		return Denied
	}
	scope := item.scope
	s.mu.Unlock()
	if err := s.allowed(ctx, subject, scope); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	now := s.now()
	if s.items[subject] != item || item.linked || !now.Before(item.expires) {
		return Denied
	}
	s.items[subject] = &entry{scope: scope, expires: now.Add(linkedLife), linked: true}
	return nil
}

// Resolve returns only the already selected scope, after rechecking policy.
func (s *Store) Resolve(ctx context.Context, subject Subject) (Scope, error) {
	if !validSubject(subject) {
		return Scope{}, Denied
	}
	s.mu.Lock()
	item := s.items[subject]
	if item == nil || !item.linked || !s.now().Before(item.expires) {
		s.mu.Unlock()
		return Scope{}, Denied
	}
	scope := item.scope
	s.mu.Unlock()
	if err := s.allowed(ctx, subject, scope); err != nil {
		return Scope{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Scope{}, err
	}
	if s.items[subject] != item || !s.now().Before(item.expires) {
		return Scope{}, Denied
	}
	return scope, nil
}

// Revoke discards any pending proof or link for an already authorized subject.
func (s *Store) Revoke(subject Subject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, subject)
}
