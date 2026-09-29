package console

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	// MinAdminTokenLen is the shortest admin token accepted, in bytes.
	MinAdminTokenLen = 16

	// tokenBytes of randomness in a session token.
	tokenBytes = 32
)

// Errors of Settings.Validate.
var (
	// ErrNoAdminToken: no admin token is configured.
	ErrNoAdminToken = errors.New("console: no admin token configured")
	// ErrShortAdminToken: the admin token is shorter than MinAdminTokenLen.
	ErrShortAdminToken = fmt.Errorf("console: admin token shorter than %d characters", MinAdminTokenLen)
)

// adminToken is the SHA-256 digest of the configured admin token. Login
// compares digests, so neither the token's contents nor its length leak
// through timing.
type adminToken [sha256.Size]byte

func newAdminToken(token string) adminToken { return sha256.Sum256([]byte(token)) }

// match reports whether token is the admin token, in constant time.
func (t adminToken) match(token string) bool {
	got := sha256.Sum256([]byte(token))

	return subtle.ConstantTimeCompare(got[:], t[:]) == 1
}

// randomToken is tokenBytes of crypto randomness, base64url.
func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("console: random: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sessionHash is what the store keeps of a session token: its SHA-256. The
// token is random, so a fast hash is enough.
func sessionHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))

	return sum[:]
}
