package trust

import (
	"errors"
	"strings"
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

func TestDeriveAppSecret(t *testing.T) {
	master := "server-master-key"

	// Deterministic for the same (app, version).
	a := DeriveAppSecret(master, "app1", 1)
	if a != DeriveAppSecret(master, "app1", 1) {
		t.Error("derivation not deterministic")
	}
	// Different per app, per version, and per master key.
	for _, other := range []string{
		DeriveAppSecret(master, "app2", 1),
		DeriveAppSecret(master, "app1", 2),
		DeriveAppSecret("other-master", "app1", 1),
	} {
		if other == a {
			t.Errorf("derived secret collided: %s", other)
		}
	}
	// Recognizable, non-empty prefix; the master key must not leak into it.
	if len(a) < 10 || a[:5] != "lbsk_" {
		t.Errorf("unexpected secret format: %q", a)
	}
	if strings.Contains(a, master) {
		t.Error("derived secret leaks the master key")
	}

	// A derived secret round-trips through Sign/Verify (the server verifies with
	// the same DeriveAppSecret it would hand the developer).
	v := NewVerifier(a, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	sig := Sign(a, "app1", "b", "m", 42, now.Unix(), "n")
	if err := v.Verify(sig, now.Unix(), now, "app1", "b", "m", 42, "n"); err != nil {
		t.Errorf("derived-secret signature rejected: %v", err)
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
