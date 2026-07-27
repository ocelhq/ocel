package cli

import (
	"regexp"
	"strings"
)

// nonSlugChars matches runs of characters not allowed in a slug.
var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// slugify derives a URL/API-safe slug from an arbitrary project name:
// lowercase, runs of non [a-z0-9] characters collapsed to a single hyphen,
// leading/trailing hyphens trimmed, truncated to 63 chars. The result is
// either empty or a valid slug (see validSlug).
func slugify(name string) string {
	lower := strings.ToLower(name)
	slug := nonSlugChars.ReplaceAllString(lower, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	return slug
}

// slugPattern mirrors the DNS-label rule the config resolver enforces (see
// slugPattern in cli/internal/projectconfig): lowercase letters, digits and
// hyphens, 1–63 characters, not hyphen-bounded. `ocel init` validates against
// it so it never writes a config the resolver would later reject.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validSlug reports whether s is a usable project slug.
func validSlug(s string) bool {
	return slugPattern.MatchString(s)
}
