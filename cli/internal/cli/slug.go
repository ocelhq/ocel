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
// either empty or a slug projectconfig.ValidSlug accepts.
func slugify(name string) string {
	lower := strings.ToLower(name)
	slug := nonSlugChars.ReplaceAllString(lower, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	return slug
}
