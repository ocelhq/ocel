// Package previewid resolves a git ref into the canonical, substrate-safe
// identity a preview environment is keyed by. It is pure: no git, no process,
// no I/O. The branch is always the identity; a PR number, when present, is a
// display label and never part of the key.
package previewid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// SourceGit marks an identity derived from a git ref. It mirrors the
// IDENTITY_SOURCE_GIT value in the provider contract without importing proto,
// keeping this module dependency-free.
const SourceGit = "git"

// hashLen is how many hex characters of the ref's sha256 are appended to
// disambiguate refs that sanitize to the same base token.
const hashLen = 8

// maxLabelLen is the DNS label limit (RFC 1035). Spelled again as
// cloud/aws/deploy.previewLabelMaxLen, which measures the whole assembled label
// ("<pointer>--<app>") and so can still refuse a pointer this one accepts.
const maxLabelLen = 63

// maxKeyLen is the longest usable Key: it doubles as the preview subdomain
// label and the preview store pointer, and a project with one app is served on
// the bare label, so the whole label is the pointer's.
const maxKeyLen = maxLabelLen

// maxBaseLen caps the sanitized base so base + "-" + hash stays within
// maxKeyLen.
const maxBaseLen = maxKeyLen - 1 - hashLen

// Identity is a resolved preview environment key.
type Identity struct {
	Key    string
	Label  string
	Source string
}

// Resolve derives a preview Identity from a git ref and an optional PR number.
// Key is the sanitized ref plus a short deterministic hash of the full ref,
// joined by a hyphen, always a valid label (see ValidateLabel) so it doubles
// as the preview subdomain label and the store pointer. The same ref always
// resolves to the same Key. Label is the PR number formatted for display (e.g.
// "pr-482"), or empty when prNumber is empty.
func Resolve(ref string, prNumber string) (Identity, error) {
	if ref == "" {
		return Identity{}, fmt.Errorf("previewid: empty ref")
	}

	base := sanitize(ref)

	// A DNS label must start with a letter, so an empty or non-letter-leading
	// base is given the "env" stem. The stem is applied before the length cap so
	// the cap bounds the final base — a digit-led ref must not blow past
	// maxBaseLen once "env-" is prepended.
	switch {
	case base == "":
		base = "env"
	case !(base[0] >= 'a' && base[0] <= 'z'):
		base = "env-" + base
	}
	if len(base) > maxBaseLen {
		base = strings.Trim(base[:maxBaseLen], "-")
	}

	sum := sha256.Sum256([]byte(ref))
	hash := hex.EncodeToString(sum[:])[:hashLen]

	key := base + "-" + hash

	label := ""
	if prNumber != "" {
		label = "pr-" + prNumber
	}

	return Identity{Key: key, Label: label, Source: SourceGit}, nil
}

// sanitize lowercases the ref and reduces it to a DNS-label-safe token:
// alphanumerics pass through, every other run of characters collapses to a
// single hyphen, and leading and trailing hyphens are trimmed.
func sanitize(ref string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(ref) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// dnsLabelPattern is the shape of a DNS label: lowercase letter start, then
// letters, digits and hyphens, not ending in a hyphen. Length is checked
// separately so an over-long name gets its own message.
var dnsLabelPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// appSeparator is what a multi-app project's preview host puts between the
// pointer and the app in one label ("<pointer>--<app>"), so a pointer carrying
// it is ambiguous: the entrypoint worker recovers the app half by matching the
// project's app names, and a pointer ending in "--<some app name>" resolves to
// a shorter pointer that names no deployment. Reserved here rather than left to
// the host, which cannot tell such a pointer apart from a real one.
//
// Keep in step with cloud/aws/deploy.previewAppSeparator, which builds the
// hostname, and APP_SEPARATOR in workers/nextjs/src/preview.ts, which parses it
// back: three modules, no shared constant.
const appSeparator = "--"

// ValidateLabel reports why s is unusable as a preview subdomain label and
// store pointer, or nil when it is usable: the shape Resolve produces, and the
// shape a persistent preview's --name must take. It is the single validator
// both paths share, and its errors are written to be shown to the user.
func ValidateLabel(s string) error {
	if !dnsLabelPattern.MatchString(s) {
		return fmt.Errorf("invalid preview name %q: use a DNS-label-safe name (lowercase letters, digits and hyphens)", s)
	}
	if strings.Contains(s, appSeparator) {
		return fmt.Errorf("invalid preview name %q: %q separates the preview from the app in the hostname it is served on (\"<preview>%s<app>\"), so a name may not contain it — use a single hyphen", s, appSeparator, appSeparator)
	}
	if len(s) > maxKeyLen {
		return fmt.Errorf("preview name %q is too long: the limit is %d characters, the DNS label it is served on", s, maxKeyLen)
	}
	return nil
}
