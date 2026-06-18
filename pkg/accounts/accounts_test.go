package accounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/kodeni-am/leaderboard/pkg/email"
)

// captureSender records the last email so tests can extract verify/reset links.
type captureSender struct {
	mu   sync.Mutex
	last email.Message
}

func (c *captureSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	c.last = m
	c.mu.Unlock()
	return nil
}

func (c *captureSender) token(marker string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := strings.Index(c.last.Text, marker)
	if i < 0 {
		return ""
	}
	rest := c.last.Text[i+len(marker):]
	if j := strings.IndexAny(rest, "\r\n "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func newSvc() (*Service, *captureSender) {
	m := NewMemStores()
	cap := &captureSender{}
	return NewService(m, m, m, cap, Config{BaseURL: "http://app"}), cap
}

func TestSignupVerifyLogin(t *testing.T) {
	ctx := context.Background()
	svc, mail := newSvc()

	u, err := svc.Signup(ctx, "Dev@Example.com", "hunter2hunter")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "dev@example.com" {
		t.Errorf("email not normalized: %q", u.Email)
	}
	if u.EmailVerified {
		t.Error("should start unverified")
	}

	// Login before verification is refused.
	if _, _, err := svc.Login(ctx, "dev@example.com", "hunter2hunter"); !errors.Is(err, ErrEmailNotVerified) {
		t.Errorf("expected ErrEmailNotVerified, got %v", err)
	}

	// Verify using the emailed token.
	tok := mail.token("/auth/verify?token=")
	if tok == "" {
		t.Fatal("no verification token in email")
	}
	if err := svc.Verify(ctx, tok); err != nil {
		t.Fatal(err)
	}
	// Token is single-use.
	if err := svc.Verify(ctx, tok); !errors.Is(err, ErrBadToken) {
		t.Errorf("verify token should be one-time, got %v", err)
	}

	// Now login works and the session resolves.
	sess, _, err := svc.Login(ctx, "dev@example.com", "hunter2hunter")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.UserFromSession(ctx, sess)
	if err != nil || got.ID != u.ID {
		t.Fatalf("UserFromSession: %v / %v", got, err)
	}

	// Logout invalidates the session.
	if err := svc.Logout(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UserFromSession(ctx, sess); !errors.Is(err, ErrNoSession) {
		t.Errorf("session should be gone after logout, got %v", err)
	}
}

func TestSignupValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc()
	if _, err := svc.Signup(ctx, "not-an-email", "longenough"); !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("bad email: %v", err)
	}
	if _, err := svc.Signup(ctx, "a@b.co", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak password: %v", err)
	}
	if _, err := svc.Signup(ctx, "dup@b.co", "longenough1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Signup(ctx, "dup@b.co", "longenough1"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate: %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	ctx := context.Background()
	svc, mail := newSvc()
	svc.Signup(ctx, "x@y.co", "correcthorse")
	svc.Verify(ctx, mail.token("/auth/verify?token="))
	if _, _, err := svc.Login(ctx, "x@y.co", "wrongpassword"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, err := svc.Login(ctx, "ghost@y.co", "whatever12"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user should be generic invalid creds: %v", err)
	}
}

func TestPasswordReset(t *testing.T) {
	ctx := context.Background()
	svc, mail := newSvc()
	svc.Signup(ctx, "r@y.co", "originalpass")
	svc.Verify(ctx, mail.token("/auth/verify?token="))
	sess, _, _ := svc.Login(ctx, "r@y.co", "originalpass")

	if err := svc.RequestReset(ctx, "r@y.co"); err != nil {
		t.Fatal(err)
	}
	resetTok := mail.token("/reset?token=")
	if resetTok == "" {
		t.Fatal("no reset token")
	}
	if err := svc.ResetPassword(ctx, resetTok, "brandnewpass"); err != nil {
		t.Fatal(err)
	}
	// Old session revoked, old password rejected, new password works.
	if _, err := svc.UserFromSession(ctx, sess); !errors.Is(err, ErrNoSession) {
		t.Errorf("reset should revoke sessions, got %v", err)
	}
	if _, _, err := svc.Login(ctx, "r@y.co", "originalpass"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password should fail: %v", err)
	}
	if _, _, err := svc.Login(ctx, "r@y.co", "brandnewpass"); err != nil {
		t.Errorf("new password should work: %v", err)
	}
	// Forgot for unknown email is a silent no-op (no enumeration).
	if err := svc.RequestReset(ctx, "nobody@nowhere.co"); err != nil {
		t.Errorf("forgot unknown email should be nil, got %v", err)
	}
}
