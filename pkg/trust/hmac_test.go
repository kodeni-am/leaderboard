package trust

import (
	"errors"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	secret := "shhh"
	v := NewVerifier(secret, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()
	sig := Sign(secret, "app1", "high", "alice", 1234.5, ts, "nonce-1")

	if err := v.Verify(sig, ts, now, "app1", "high", "alice", 1234.5, "nonce-1"); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	secret := "shhh"
	v := NewVerifier(secret, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()
	sig := Sign(secret, "app1", "high", "alice", 100, ts, "n")

	// Tampered score must fail.
	if err := v.Verify(sig, ts, now, "app1", "high", "alice", 999999, "n"); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered score: got %v, want ErrBadSignature", err)
	}
	// Wrong secret must fail.
	other := NewVerifier("different", time.Minute)
	if err := other.Verify(sig, ts, now, "app1", "high", "alice", 100, "n"); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong secret: got %v, want ErrBadSignature", err)
	}
}

func TestVerifyRejectsStale(t *testing.T) {
	secret := "shhh"
	v := NewVerifier(secret, time.Minute)
	signedAt := time.Unix(1_700_000_000, 0)
	ts := signedAt.Unix()
	sig := Sign(secret, "app1", "high", "alice", 100, ts, "n")

	// 10 minutes later, outside the 1-minute skew window -> replay rejected.
	later := signedAt.Add(10 * time.Minute)
	if err := v.Verify(sig, ts, later, "app1", "high", "alice", 100, "n"); !errors.Is(err, ErrStaleSubmission) {
		t.Errorf("stale submission: got %v, want ErrStaleSubmission", err)
	}
}
