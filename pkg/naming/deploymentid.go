package naming

import (
	"fmt"
	"regexp"
)

var deploymentIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func ValidateDeploymentID(value string) error {
	if value == "" {
		return fmt.Errorf("deployment id is required")
	}
	if !deploymentIDPattern.MatchString(value) {
		return fmt.Errorf("deployment id %q must be 32 lowercase hex characters", value)
	}
	return nil
}
