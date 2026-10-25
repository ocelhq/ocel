package project

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

func DeriveSlug(name string) string {
	slug := nonSlugChars.ReplaceAllString(strings.ToLower(name), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	return slug
}

func ValidateSlug(s string) error {
	if !dnsLabelPattern.MatchString(s) {
		return fmt.Errorf("%q must be a DNS label: lowercase letters, digits and hyphens, 1–63 characters, not starting or ending with a hyphen", s)
	}
	if strings.Contains(s, naming.FieldSeparator) {
		return fmt.Errorf("%q may not contain %q: use a single hyphen", s, naming.FieldSeparator)
	}
	return nil
}
