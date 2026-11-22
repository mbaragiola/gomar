package chatlink

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type policyFunc func(context.Context, Subject, Scope) (bool, error)

func (fn policyFunc) CanReadPayableTotals(ctx context.Context, subject Subject, scope Scope) (bool, error) {
	return fn(ctx, subject, scope)
}

func TestChatLinkLifecycle(t *testing.T) {
	ctx := context.Background()
	subject := Subject{CLI, "synthetic-client"}
	scope := Scope{7, 11, 13}
	now := time.Date(2028, 3, 1, 12, 0, 0, 0, time.UTC)
	allow, fail := true, false
	s, err := New(func() time.Time { return now }, policyFunc(func(_ context.Context, got Subject, selected Scope) (bool, error) {
		if got != subject || selected != scope {
			t.Fatalf("policy received unexpected identity or selection")
		}
		if fail {
			return false, errors.New("policy-private diagnostic")
		}
		return allow, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	denied := func(subject Subject, want error) {
		t.Helper()
		got, err := s.Resolve(ctx, subject)
		if got != (Scope{}) || !errors.Is(err, want) {
			t.Fatalf("resolution = %#v, %v; want blank and %v", got, err, want)
		}
	}
	denied(subject, Denied)
	proof, err := s.Begin(ctx, subject, scope)
	if err != nil {
		t.Fatal(err)
	}
	if bytes, err := base64.RawURLEncoding.DecodeString(proof); err != nil || len(bytes) != proofBytes {
		t.Fatal("wrong proof format")
	}
	denied(subject, Denied)
	if err := s.Complete(ctx, subject, proof); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, subject, proof); !errors.Is(err, Denied) {
		t.Fatalf("already consumed proof returned %v", err)
	}
	if got, err := s.Resolve(ctx, subject); err != nil || got != scope {
		t.Fatalf("read selection = %#v, %v", got, err)
	}
	allow = false
	denied(subject, Denied)
	fail = true
	denied(subject, Unavailable)
	fail, allow = false, true
	s.Revoke(subject)
	denied(subject, Denied)
}

func TestChatLinkExpirationAndScope(t *testing.T) {
	ctx := context.Background()
	cli := Subject{CLI, "same-opaque-label"}
	wa := Subject{WhatsApp, cli.Principal}
	scope := Scope{7, 11, 13}
	now := time.Date(2028, 3, 1, 12, 0, 0, 0, time.UTC)
	s, err := New(func() time.Time { return now }, policyFunc(func(context.Context, Subject, Scope) (bool, error) {
		return true, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.Begin(ctx, cli, scope)
	if err != nil {
		t.Fatal(err)
	}
	if replacement, err := s.Begin(ctx, cli, Scope{7, 11, 14}); replacement != "" || !errors.Is(err, Denied) {
		t.Fatal("a pending scope could be replaced")
	}
	if err := s.Complete(ctx, wa, proof); !errors.Is(err, Denied) {
		t.Fatal("proof was accepted under a different channel")
	}
	now = now.Add(pendingLife)
	if err := s.Complete(ctx, cli, proof); !errors.Is(err, Denied) {
		t.Fatal("pending proof accepted at expiry")
	}
	fresh, err := s.Begin(ctx, cli, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, cli, fresh); err != nil {
		t.Fatal(err)
	}
	now = now.Add(linkedLife - time.Nanosecond)
	if got, err := s.Resolve(ctx, cli); got != scope || err != nil {
		t.Fatalf("link failed before expiry: %#v, %v", got, err)
	}
	now = now.Add(time.Nanosecond)
	if got, err := s.Resolve(ctx, cli); got != (Scope{}) || !errors.Is(err, Denied) {
		t.Fatal("link accepted at expiry")
	}
}

func TestChatLinkConcurrentConsumptionAndCapacity(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2028, 3, 1, 12, 0, 0, 0, time.UTC)
	scope := Scope{7, 11, 13}
	s, err := New(func() time.Time { return now }, policyFunc(func(context.Context, Subject, Scope) (bool, error) {
		return true, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxEntries; i++ {
		if _, err := s.Begin(ctx, Subject{CLI, fmt.Sprint(i)}, scope); err != nil {
			t.Fatal(err)
		}
	}
	subject := Subject{WhatsApp, "linked-synthetic"}
	if _, err := s.Begin(ctx, subject, scope); !errors.Is(err, Capacity) {
		t.Fatal("expected bounded capacity")
	}
	now = now.Add(pendingLife)
	proof, err := s.Begin(ctx, subject, scope)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { results <- s.Complete(ctx, subject, proof) })
	}
	wg.Wait()
	close(results)
	ok, denied := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, Denied):
			denied++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || denied != 1 {
		t.Fatalf("concurrent completion results = %d accepted, %d denied", ok, denied)
	}
}
