package cli

import "fmt"

// ExitError signals that Execute should exit with Code instead of the
// generic failure path (which prints "Error: ..." and exits 1). Used when a
// spawned child's exit code must propagate verbatim.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}
