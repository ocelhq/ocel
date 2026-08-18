package edge

import (
	"strings"
	"testing"
	"time"
)

func TestOutstandingError(t *testing.T) {
	t.Parallel()

	err := &OutstandingError{
		Because: "API Gateway paces deletions",
		Waited:  14*time.Minute + 30*time.Second,
		Items: []Outstanding{
			{Kind: "REST API", Name: "api1"},
			{Kind: "REST API", Name: "api2"},
		},
	}

	got := err.Error()
	for _, want := range []string{
		"API Gateway paces deletions",
		"about 14m30s",
		"re-run the same command",
		"\n  • REST API api1",
		"\n  • REST API api2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error = %q, want it to contain %q", got, want)
		}
	}
	if lines := strings.Count(got, "\n"); lines != len(err.Items) {
		t.Errorf("error spans %d lines past the first, want one per outstanding item", lines)
	}
}
