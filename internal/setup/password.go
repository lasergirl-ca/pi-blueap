package setup

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Web UI password hashing (PLAN.md section 8.12: argon2id). The stored form is
// the PHC string format, $argon2id$v=19$m=…,t=…,p=…$salt$hash, so the
// parameters travel with the hash and can be raised later.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB: 64 MiB, comfortable on a Pi 4 with 1 GB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16

	// MinPasswordLen is the shortest web password setup accepts.
	MinPasswordLen = 8
)

var b64 = base64.RawStdEncoding

// HashPassword returns the argon2id PHC string for password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks password against a PHC string from HashPassword.
func VerifyPassword(password, phc string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("not an argon2id hash")
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, fmt.Errorf("argon2 version %q not supported", parts[2])
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("argon2 parameters %q: %w", parts[3], err)
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// CheckNewPassword is the rule setup applies to a new web password.
func CheckNewPassword(pw string) error {
	if len([]rune(pw)) < MinPasswordLen {
		return fmt.Errorf("at least %d characters", MinPasswordLen)
	}
	return nil
}
