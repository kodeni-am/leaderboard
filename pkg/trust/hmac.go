// Package trust is the SP7 anti-cheat layer. v1 provides server-side
// verification of HMAC-signed score submissions plus a freshness window to stop
// replay. The client signs a canonical message with a shared secret; the server
// recomputes and compares in constant time. Statistical anomaly detection is a
// documented follow-on that consumes the same durable log.
package trust

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	ErrBadSignature    = errors.New("trust: bad signature")
	ErrStaleSubmission = errors.New("trust: submission timestamp outside allowed skew")
)

// canonical builds the deterministic message that both sides sign. Field order
// and the newline separator are part of the contract.
func canonical(app, board, member string, score float64, ts int64, nonce string) string {
	return strings.Join([]string{
		app,
		board,
		member,
		strconv.FormatFloat(score, 'f', -1, 64),
		strconv.FormatInt(ts, 10),
		nonce,
	}, "\n")
}

// Sign returns the hex HMAC-SHA256 signature for a submission. Clients use this
// (or an equivalent in their language) to sign before submitting.
func Sign(secret, app, board, member string, score float64, ts int64, nonce string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical(app, board, member, score, ts, nonce)))
	return hex.EncodeToString(mac.Sum(nil))
}

// DeriveAppSecret derives a per-app signing secret from the server's master key
// (the SIGNING_SECRET env), the app id, and a rotation version. It is
// deterministic — the server recomputes it on demand to verify, and the
// dashboard reveals it to the app owner — so no per-app secret is ever stored.
// Bumping the version rotates the secret (invalidating signatures made with the
// previous one). The master key is never exposed to tenants.
func DeriveAppSecret(master, appID string, version int) string {
	mac := hmac.New(sha256.New, []byte(master))
	mac.Write([]byte("openleaderboard/app-signing/v1\n"))
	mac.Write([]byte(appID))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strconv.Itoa(version)))
	// "lbsk_" = leaderboard signing key, a recognizable prefix for the developer.
	return "lbsk_" + hex.EncodeToString(mac.Sum(nil))
}

// Verifier validates signed submissions against a shared secret.
type Verifier struct {
	secret  string
	maxSkew time.Duration
}

func NewVerifier(secret string, maxSkew time.Duration) *Verifier {
	if maxSkew <= 0 {
		maxSkew = 5 * time.Minute
	}
	return &Verifier{secret: secret, maxSkew: maxSkew}
}

// Verify checks the signature and that ts is within maxSkew of now.
func (v *Verifier) Verify(sig string, ts int64, now time.Time, app, board, member string, score float64, nonce string) error {
	submitted := time.Unix(ts, 0)
	skew := now.Sub(submitted)
	if skew < 0 {
		skew = -skew
	}
	if skew > v.maxSkew {
		return ErrStaleSubmission
	}
	expected := Sign(v.secret, app, board, member, score, ts, nonce)
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return ErrBadSignature
	}
	return nil
}
