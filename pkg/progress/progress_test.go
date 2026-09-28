package progress

import (
	"errors"
	"slices"
	"testing"
	"time"
)

type heard struct {
	said, warned []string
}

func (h *heard) Say(message string) { h.said = append(h.said, message) }

func (h *heard) Warn(message string) { h.warned = append(h.warned, message) }

func (*heard) Error(string) {}

func (*heard) Detail(string) {}

func (*heard) Debug(string) {}

func (*heard) Span(string, time.Time, time.Time, error, ...Attr) {}

func TestAHeededWarningIsAWarnAndNotAFailure(t *testing.T) {
	t.Parallel()

	progress := &heard{}
	if err := Heeded(Warned(errors.New("the old binding outlived its release")), progress); err != nil {
		t.Fatalf("Heeded(a warning) = %v, want nil", err)
	}
	if want := []string{"the old binding outlived its release"}; !slices.Equal(progress.warned, want) {
		t.Errorf("warned %q, want %q", progress.warned, want)
	}
	if len(progress.said) != 0 {
		t.Errorf("said %q, want the warning only as a warning", progress.said)
	}
}

func TestAHeededFailureIsReturnedUnspoken(t *testing.T) {
	t.Parallel()

	progress := &heard{}
	failure := errors.New("the zone is gone")
	if err := Heeded(failure, progress); !errors.Is(err, failure) {
		t.Fatalf("Heeded(a failure) = %v, want %v", err, failure)
	}
	if len(progress.said)+len(progress.warned) != 0 {
		t.Errorf("a failure was spoken: said %q, warned %q", progress.said, progress.warned)
	}
}
