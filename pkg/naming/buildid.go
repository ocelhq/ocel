package naming

import (
	"fmt"
	"regexp"
)

var buildIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ValidateBuildID(value string) error {
	if value == "" {
		return fmt.Errorf("build id is required")
	}
	if !buildIDPattern.MatchString(value) {
		return fmt.Errorf("build id %q must be 32 lowercase hex characters", value)
	}
	return nil
}
