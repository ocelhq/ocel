package cli

import "fmt"

// maxTagLen bounds a deployment tag so it stays readable in `ocel deployments
// ls` and easy to retype for a rollback.
const maxTagLen = 64

// validateTag enforces the deployment-tag format: 1–64 characters drawn from
// [A-Za-z0-9._-]. An empty tag is the "untagged" default and is always valid,
// so callers can validate unconditionally.
func validateTag(tag string) error {
	if tag == "" {
		return nil
	}
	if len(tag) > maxTagLen {
		return fmt.Errorf("tag must be at most %d characters (got %d)", maxTagLen, len(tag))
	}
	for _, r := range tag {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("tag %q has an invalid character %q; use only letters, digits, '.', '_' and '-'", tag, r)
	}
	return nil
}
