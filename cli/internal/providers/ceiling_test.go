package providers

import (
	"io"
	"strings"
	"testing"
)

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestABodyOverTheCeilingIsRefusedRatherThanTruncated(t *testing.T) {
	t.Parallel()

	err := fill(io.Discard, endless{})
	if err == nil {
		t.Fatal("fill() error = nil, want a body past the ceiling refused rather than cut short")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("error %q does not say the member outgrew the ceiling", err.Error())
	}
}

func TestABodyUnderTheCeilingSpillsWhole(t *testing.T) {
	t.Parallel()

	var got strings.Builder
	if err := fill(&got, strings.NewReader("provider")); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if got.String() != "provider" {
		t.Fatalf("fill wrote %q, want %q", got.String(), "provider")
	}
}
