package deploy

import "strings"

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
