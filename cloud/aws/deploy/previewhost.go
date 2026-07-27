package deploy

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
)

// previewLabelMaxLen is the DNS label limit (RFC 1035).
const previewLabelMaxLen = 63

// previewRouteSuffixLen is how much of a preview hostname's first label the
// suffix claims, leaving previewPointerMaxLen for the pointer.
const previewRouteSuffixLen = 1 + previewRouteSuffixHashLen

const previewRouteSuffixHashLen = 10

// previewPointerMaxLen is the longest pointer that still fits a DNS label once
// the suffix is appended. It mirrors the pointer cap in cli/internal/previewid
// (a separate Go module): keep the two in step.
const previewPointerMaxLen = previewLabelMaxLen - previewRouteSuffixLen

var previewRouteEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// previewRouteSuffix is the "-<hash>" tail that makes a preview hostname unique
// across every project and app sharing a zone, so each pointer gets its own
// exact route instead of a project-wide wildcard. The serving worker is handed
// this suffix as an env var and strips it to recover the pointer, so it must
// stay a pure function of (slug, app). The fields are hashed length-delimited
// because plain concatenation would give slug="a", app="b-c" and slug="a-b",
// app="c" the same hostname.
func previewRouteSuffix(slug, app string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s", len(slug), slug, len(app), app)))
	return "-" + strings.ToLower(previewRouteEncoding.EncodeToString(sum[:]))[:previewRouteSuffixHashLen]
}

// previewRouteHost is the hostname a preview pointer is served on:
// "<pointer><suffix>.<base>". base is the preview base domain with the leading
// "*." already stripped (see previewBaseDomain). Returns "" when either half is
// missing, leaving no usable hostname.
func previewRouteHost(slug, app, pointer, base string) string {
	if pointer == "" || base == "" {
		return ""
	}
	return pointer + previewRouteSuffix(slug, app) + "." + base
}
