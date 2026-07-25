package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxSafeNamePrefixLen caps a safe name so Pulumi's random suffix (appended to
// a *Prefix field) still fits inside the strictest AWS physical-name limit (S3
// bucket and RDS identifier are both 63). Kept well under to leave room for the
// suffix and any per-resource infix ("-instance-").
const maxSafeNamePrefixLen = 40

// safeName maps a manifest logical name (`<type>_<id>`, e.g. "bucket_uploads")
// to a DNS/identifier-safe token usable as an S3 bucket or RDS identifier
// prefix: lowercased, every char outside [a-z0-9-] (notably the underscore
// separator) replaced with "-", consecutive "-" collapsed, leading/trailing
// "-" trimmed, and prefixed with "a" when the result doesn't start with a
// letter (RDS identifiers must start with a letter). The result is capped so
// Pulumi's random suffix stays within AWS name limits. It is pure.
func safeName(logicalName string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(logicalName) {
		safe := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !safe {
			r = '-'
		}
		if r == '-' {
			if prevDash || b.Len() == 0 {
				continue
			}
			prevDash = true
			b.WriteRune('-')
			continue
		}
		prevDash = false
		b.WriteRune(r)
	}
	name := strings.TrimRight(b.String(), "-")

	if name == "" {
		name = "a"
	}
	if first := name[0]; first < 'a' || first > 'z' {
		name = "a" + name
	}
	if len(name) > maxSafeNamePrefixLen {
		name = strings.TrimRight(name[:maxSafeNamePrefixLen], "-")
	}
	return name
}

// maxLambdaNameLen is AWS's limit on a Lambda function name, and
// lambdaAutonameSuffixLen is what Pulumi appends to a resource name to autoname
// one: a hyphen plus seven random characters.
const (
	maxLambdaNameLen        = 64
	lambdaAutonameSuffixLen = 8
)

// lambdaResourceName is the Pulumi resource name a function is registered
// under, derived from its manifest logical name. Pulumi autonames the physical
// Lambda from it, so a logical name long enough to push that past AWS's limit
// (a deep Next.js route under a long app name reaches it easily) is clamped
// with a hash of the full name appended: the result stays inside the limit,
// stays unique across two routes that share a prefix, and stays deterministic
// across deploys, so a function is not replaced on every up.
//
// Only the resource name is shortened. The logical name still keys the
// function's artifact, its stack export and the edge worker's route table, so
// nothing downstream has to know this happened.
func lambdaResourceName(logicalName string) string {
	max := maxLambdaNameLen - lambdaAutonameSuffixLen
	if len(logicalName) <= max {
		return logicalName
	}
	sum := sha256.Sum256([]byte(logicalName))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	return logicalName[:max-len(suffix)] + suffix
}

// physicalNamePrefix builds the DNS/identifier-safe *Prefix value for a
// resource's physical AWS name from its logical name. Pulumi appends its own
// random suffix to this prefix, preserving per-deploy uniqueness while keeping
// the human-readable, safe stem. An optional infix distinguishes a resource's
// sub-resources that share a logical name (e.g. an RDS cluster's instance). It
// is pure.
//
// Switching a resource from autonaming to an explicit prefix changes its
// physical name, so Pulumi replaces any already-deployed resource on the next
// up; that is acceptable here (greenfield, nothing in production yet).
func physicalNamePrefix(logicalName, infix string) string {
	prefix := safeName(logicalName) + "-"
	if infix != "" {
		prefix += infix + "-"
	}
	return prefix
}
