// Package previewid resolves a git ref into the canonical, substrate-safe
// identity a preview environment is keyed by. It is pure: no git, no process,
// no I/O. The branch is always the identity; a PR number, when present, is a
// display label and never part of the key.
package previewid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SourceGit marks an identity derived from a git ref. It mirrors the
// IDENTITY_SOURCE_GIT value in the provider contract without importing proto,
// keeping this module dependency-free.
const SourceGit = "git"

// maxBaseLen caps the sanitized ref token so the full key stays comfortably
// within Postgres identifier and stack-name limits once the hash is appended.
const maxBaseLen = 40

// hashLen is how many hex characters of the ref's sha256 are appended to
// disambiguate refs that sanitize to the same base token.
const hashLen = 8

// Identity is a resolved preview environment key.
type Identity struct {
	Key    string
	Label  string
	Source string
}

// Resolve derives a preview Identity from a git ref and an optional PR number.
// Key is a substrate-safe canonical key: the sanitized ref plus a short
// deterministic hash of the full ref. It is a valid UNQUOTED Postgres
// identifier and Pulumi stack-name token (matches `^[a-z_][a-z0-9_]*$`), so it
// can be used directly as an ephemeral logical database name. Label is the PR
// number formatted for display (e.g. "pr-482"), or empty when prNumber is
// empty.
func Resolve(ref string, prNumber string) (Identity, error) {
	if ref == "" {
		return Identity{}, fmt.Errorf("previewid: empty ref")
	}

	base := sanitize(ref)
	if len(base) > maxBaseLen {
		base = strings.Trim(base[:maxBaseLen], "_")
	}

	sum := sha256.Sum256([]byte(ref))
	hash := hex.EncodeToString(sum[:])[:hashLen]

	// A valid unquoted Postgres identifier must start with a letter or
	// underscore, so an empty or digit-leading base is prefixed with "env_".
	if base == "" || !(base[0] >= 'a' && base[0] <= 'z') {
		base = "env_" + base
	}
	key := base + "_" + hash

	label := ""
	if prNumber != "" {
		label = "pr-" + prNumber
	}

	return Identity{Key: key, Label: label, Source: SourceGit}, nil
}

// sanitize lowercases the ref and reduces it to alphanumerics and underscores:
// every other run of characters collapses to a single underscore, and leading
// and trailing underscores are trimmed. Underscore is valid unquoted in both
// Postgres identifiers and Pulumi stack names.
func sanitize(ref string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(ref) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
			continue
		}
		if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
