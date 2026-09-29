package console

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters of the admin token hash: OWASP's minimum (19 MiB,
// two passes, one lane). The token is random when generated; the hash
// protects one set by hand in BACKPLANE_ADMIN_TOKEN.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16

	// tokenBytes of randomness in an admin token and a session token.
	tokenBytes = 32
	// adminPrefix marks a generated admin token.
	adminPrefix = "bpat_"
)

var errHashFormat = errors.New("console: admin token hash: not an argon2id PHC string")

// randomToken is tokenBytes of crypto randomness, base64url.
func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("console: random: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newAdminToken is a fresh admin token.
func newAdminToken() (string, error) {
	t, err := randomToken()

	return adminPrefix + t, err
}

// hashToken is the argon2id PHC string of token:
// $argon2id$v=19$m=<memory>,t=<time>,p=<threads>$<salt>$<key>.
func hashToken(token string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("console: random: %w", err)
	}

	key := argon2.IDKey([]byte(token), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// verifyToken reports whether token matches the PHC string, comparing in
// constant time with the parameters the hash was made with.
func verifyToken(phc, token string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errHashFormat
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errHashFormat
	}

	var (
		memory, passes uint32
		threads        uint8
	)

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil {
		return false, errHashFormat
	}

	enc := base64.RawStdEncoding

	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, errHashFormat
	}

	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errHashFormat
	}

	keyLen := uint32(len(want)) //nolint:gosec // length of a decoded key
	got := argon2.IDKey([]byte(token), salt, passes, memory, threads, keyLen)

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// sessionHash is what the store keeps of a session token: its SHA-256. The
// token is random, so a fast hash is enough.
func sessionHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))

	return sum[:]
}
