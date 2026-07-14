// Package accounts owns credentials: passwords, sessions, API tokens, and the one
// function that resolves a request to a policy.Principal.
package accounts

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// ErrBadCredentials is the ONLY error a caller may surface from a failed login.
//
// It does not distinguish "no such user" from "wrong password", because the difference
// is a user-enumeration oracle: an attacker who can tell the two apart can harvest the
// registered-address list of the whole event.
var ErrBadCredentials = errors.New("accounts: invalid email or password")

// Argon2id parameters. RFC 9106's second recommended profile (64 MiB, t=3), which is
// the one to pick when 2 GiB per hash is not available — and it is not, because a CTF
// login spike is hundreds of concurrent logins on a box that is also serving submits.
//
// The cost is stored IN the hash string, so raising these values later does not
// invalidate existing hashes: an old hash still verifies against its own recorded
// parameters and is rehashed on the next successful login, exactly like bcrypt is.
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// argonThreads is the parallelism lane count. Bounded so that a machine with 64 cores
// does not mint hashes that a 4-core replica then has to verify at a different cost.
func argonThreads() uint8 {
	if n := runtime.NumCPU(); n < 4 {
		return uint8(n) //nolint:gosec // NumCPU is >= 1 and < 4 here
	}
	return 4
}

// Hash produces an Argon2id PHC string. New passwords are always Argon2id.
func Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("accounts: refusing to hash an empty password")
	}

	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("accounts: salt: %w", err)
	}

	p := argonThreads()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, p, argonKeyLen)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, p,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify checks a password against a stored hash, in either format.
//
// Imported archives carry bcrypt hashes. Shipping Argon2id-only would lock out every
// imported user, which is the kind of thing discovered on the morning someone migrates a
// live event — so bcrypt still verifies.
//
// rehash reports that the stored hash is not the format or cost we mint today. The
// plaintext is in hand exactly once, here, so upgrading it is free; the caller is
// expected to write the new hash back. The bcrypt population then drains to zero on its
// own, with no migration script and no mass password-reset email.
func Verify(stored, password string) (ok, rehash bool) {
	switch {
	case strings.HasPrefix(stored, "$argon2id$"):
		ok = verifyArgon2id(stored, password)
		// Same algorithm, but minted under different parameters: worth an upgrade.
		return ok, ok && argonParamsStale(stored)

	case strings.HasPrefix(stored, "$2a$"), strings.HasPrefix(stored, "$2b$"),
		strings.HasPrefix(stored, "$2y$"):
		err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(password))
		// A correct bcrypt password is always worth rehashing: it is only here at all
		// because it came from an import.
		return err == nil, err == nil

	default:
		// An unrecognized or empty hash (an OAuth-only account, a corrupt row) verifies
		// against nothing. It must never verify against everything.
		return false, false
	}
}

func verifyArgon2id(stored, password string) bool {
	p, salt, want, err := parsePHC(stored)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want))) //nolint:gosec // len of a decoded digest
	return subtle.ConstantTimeCompare(got, want) == 1
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func argonParamsStale(stored string) bool {
	p, _, _, err := parsePHC(stored)
	if err != nil {
		return false
	}
	return p.memory != argonMemory || p.time != argonTime || p.threads != argonThreads()
}

// parsePHC reads $argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>.
//
// Every failure is a hard error rather than a default: a hash we cannot parse must not
// silently become a hash that matches nothing OR everything. The caller turns it into
// "verification failed", which is the safe end.
func parsePHC(s string) (p argonParams, salt, key []byte, err error) {
	parts := strings.Split(s, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, key]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("accounts: not an argon2id hash")
	}

	var version int
	if _, verr := fmt.Sscanf(parts[2], "v=%d", &version); verr != nil {
		return p, nil, nil, fmt.Errorf("accounts: bad argon2 version: %w", verr)
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("accounts: unsupported argon2 version %d", version)
	}

	if _, perr := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); perr != nil {
		return p, nil, nil, fmt.Errorf("accounts: bad argon2 params: %w", perr)
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, nil, nil, fmt.Errorf("accounts: bad argon2 salt: %w", err)
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return p, nil, nil, fmt.Errorf("accounts: bad argon2 key: %w", err)
	}
	if len(salt) == 0 || len(key) == 0 {
		return p, nil, nil, errors.New("accounts: empty argon2 salt or key")
	}
	return p, salt, key, nil
}
