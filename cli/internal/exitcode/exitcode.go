package exitcode

import (
	"context"
	"errors"
	"fmt"
)

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

const Interrupt = 130

func Of(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	if errors.Is(err, context.Canceled) {
		return Interrupt, true
	}
	return 0, false
}
