package exitcode

import (
	"context"
	"errors"
	"fmt"
)

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

func (e *ExitError) Unwrap() error { return e.Err }

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
