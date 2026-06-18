package accounts

import (
	"context"
	"regexp"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/email"
	"golang.org/x/crypto/bcrypt"
)

const (
	purposeVerify = "verify"
	purposeReset  = "reset"
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Config tunes the accounts service.
type Config struct {
	BaseURL    string        // public origin used in email links
	SessionTTL time.Duration // default 30d
	VerifyTTL  time.Duration // default 24h
	ResetTTL   time.Duration // default 1h
}

// Service implements the account flows over the three stores + an email sender.
type Service struct {
	users    UserStore
	sessions SessionStore
	tokens   TokenStore
	mail     email.Sender
	cfg      Config
}

func NewService(users UserStore, sessions SessionStore, tokens TokenStore, mail email.Sender, cfg Config) *Service {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.VerifyTTL == 0 {
		cfg.VerifyTTL = 24 * time.Hour
	}
	if cfg.ResetTTL == 0 {
		cfg.ResetTTL = time.Hour
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:8080"
	}
	return &Service{users: users, sessions: sessions, tokens: tokens, mail: mail, cfg: cfg}
}

// SessionTTL exposes the configured session lifetime (for cookie max-age).
func (s *Service) SessionTTL() time.Duration { return s.cfg.SessionTTL }

// Signup creates an unverified user and emails a verification link.
func (s *Service) Signup(ctx context.Context, emailAddr, password string) (User, error) {
	emailAddr = normalizeEmail(emailAddr)
	if !emailRe.MatchString(emailAddr) {
		return User{}, ErrInvalidEmail
	}
	if len(password) < 8 {
		return User{}, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	id, err := newID("usr_")
	if err != nil {
		return User{}, err
	}
	u := User{ID: id, Email: emailAddr, PasswordHash: string(hash), CreatedAt: time.Now().UTC()}
	if err := s.users.CreateUser(ctx, u); err != nil {
		return User{}, err
	}
	if err := s.sendVerification(ctx, u); err != nil {
		return u, err
	}
	return u, nil
}

func (s *Service) sendVerification(ctx context.Context, u User) error {
	tok, err := s.tokens.Issue(ctx, purposeVerify, u.ID, s.cfg.VerifyTTL)
	if err != nil {
		return err
	}
	link := s.cfg.BaseURL + "/auth/verify?token=" + tok
	body := "Welcome to OpenLeaderboard!\n\nConfirm your email to activate your account:\n" + link + "\n\nThis link expires in 24 hours."
	return s.mail.Send(ctx, email.Message{To: u.Email, Subject: "Verify your email", Text: body})
}

// Verify consumes a verification token and marks the email verified.
func (s *Service) Verify(ctx context.Context, token string) error {
	uid, err := s.tokens.Consume(ctx, purposeVerify, token)
	if err != nil {
		return err
	}
	u, err := s.users.GetByID(ctx, uid)
	if err != nil {
		return err
	}
	u.EmailVerified = true
	return s.users.Update(ctx, u)
}

// ResendVerification re-sends the link. It does not reveal whether the email
// exists (no enumeration) and is a no-op if already verified.
func (s *Service) ResendVerification(ctx context.Context, emailAddr string) error {
	u, err := s.users.GetByEmail(ctx, normalizeEmail(emailAddr))
	if err == ErrUserNotFound || (err == nil && u.EmailVerified) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.sendVerification(ctx, u)
}

// Login verifies credentials and a verified email, then creates a session.
func (s *Service) Login(ctx context.Context, emailAddr, password string) (token string, u User, err error) {
	u, err = s.users.GetByEmail(ctx, normalizeEmail(emailAddr))
	if err != nil {
		// Constant-ish work to blunt user enumeration via timing.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$0000000000000000000000000000000000000000000000000000"), []byte(password))
		return "", User{}, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return "", User{}, ErrInvalidCredentials
	}
	if !u.EmailVerified {
		return "", User{}, ErrEmailNotVerified
	}
	token, err = s.sessions.Create(ctx, u.ID, s.cfg.SessionTTL)
	if err != nil {
		return "", User{}, err
	}
	return token, u, nil
}

// Logout revokes a session.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.sessions.Delete(ctx, token)
}

// RequestReset emails a password-reset link. Always returns nil for existing or
// missing emails (no enumeration).
func (s *Service) RequestReset(ctx context.Context, emailAddr string) error {
	u, err := s.users.GetByEmail(ctx, normalizeEmail(emailAddr))
	if err == ErrUserNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	tok, err := s.tokens.Issue(ctx, purposeReset, u.ID, s.cfg.ResetTTL)
	if err != nil {
		return err
	}
	link := s.cfg.BaseURL + "/reset?token=" + tok
	body := "A password reset was requested for your OpenLeaderboard account.\n\nReset it here:\n" + link + "\n\nThis link expires in 1 hour. If you didn't request this, ignore this email."
	return s.mail.Send(ctx, email.Message{To: u.Email, Subject: "Reset your password", Text: body})
}

// ResetPassword consumes a reset token, sets the new password, and revokes all
// of the user's existing sessions.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if len(newPassword) < 8 {
		return ErrWeakPassword
	}
	uid, err := s.tokens.Consume(ctx, purposeReset, token)
	if err != nil {
		return err
	}
	u, err := s.users.GetByID(ctx, uid)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	if err := s.users.Update(ctx, u); err != nil {
		return err
	}
	return s.sessions.DeleteAllForUser(ctx, uid)
}

// UserFromSession resolves a session token to its user.
func (s *Service) UserFromSession(ctx context.Context, token string) (User, error) {
	uid, err := s.sessions.UserID(ctx, token)
	if err != nil {
		return User{}, err
	}
	return s.users.GetByID(ctx, uid)
}
