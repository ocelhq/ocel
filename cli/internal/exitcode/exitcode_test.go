package exitcode

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestOfMapsAnExitErrorToItsCode(t *testing.T) {
	t.Parallel()

	code, ok := Of(fmt.Errorf("build failed: %w", &ExitError{Code: 7}))
	if !ok || code != 7 {
		t.Errorf("Of = (%d, %v), want (7, true)", code, ok)
	}
}

func TestOfMapsCancellationToAnInterrupt(t *testing.T) {
	t.Parallel()

	code, ok := Of(fmt.Errorf("read ocel.config.ts: %w", context.Canceled))
	if !ok || code != Interrupt {
		t.Errorf("Of = (%d, %v), want (%d, true)", code, ok, Interrupt)
	}
}

func TestOfLeavesOrdinaryErrorsAlone(t *testing.T) {
	t.Parallel()

	if code, ok := Of(errors.New("boom")); ok {
		t.Errorf("Of = (%d, true), want no mapping so the error is printed and reported as 1", code)
	}
	if code, ok := Of(context.DeadlineExceeded); ok {
		t.Errorf("Of = (%d, true), want a timeout not to look like a Ctrl-C", code)
	}
	if code, ok := Of(nil); ok {
		t.Errorf("Of = (%d, true), want no mapping for a nil error", code)
	}
}
