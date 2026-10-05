package telemetry

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var tableRowName = regexp.MustCompile("(?m)^\\| `([^`]+)`\\s*\\|")

func readTelemetryPage(t *testing.T) string {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("..", "..", "..", "www", "content", "docs", "telemetry.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	return string(page)
}

func listNamesUnder(t *testing.T, page, heading string) []string {
	t.Helper()
	_, rest, found := strings.Cut(page, "\n## "+heading+"\n")
	if !found {
		t.Fatalf("telemetry page has no %q section", heading)
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	var names []string
	for _, match := range tableRowName.FindAllStringSubmatch(section, -1) {
		names = append(names, match[1])
	}
	slices.Sort(names)
	return names
}

func TestTheTelemetryPageDocumentsExactlyTheAllowedEvents(t *testing.T) {
	got := listNamesUnder(t, readTelemetryPage(t), "Events")

	if want := slices.Sorted(slices.Values(allowedEvents)); !slices.Equal(got, want) {
		t.Errorf("events on the page = %v, want exactly the allowlist %v", got, want)
	}
}

func TestTheTelemetryPageDocumentsExactlyTheAllowedProperties(t *testing.T) {
	got := listNamesUnder(t, readTelemetryPage(t), "Properties")

	if want := slices.Sorted(slices.Values(allowedProperties)); !slices.Equal(got, want) {
		t.Errorf("properties on the page = %v, want exactly the allowlist %v", got, want)
	}
}

func TestTheTelemetryPageNeverMentionsDoNotTrack(t *testing.T) {
	if page := readTelemetryPage(t); strings.Contains(strings.ToUpper(page), "DO_NOT_TRACK") {
		t.Error("telemetry page mentions DO_NOT_TRACK, which is honoured silently and never documented")
	}
}

func TestTheTelemetryPageNamesTheSwitchesTheBannerPromises(t *testing.T) {
	page := readTelemetryPage(t)

	for _, want := range []string{EnvVar + "=0", EnvVar + "=false", EnvVar + "=" + debugValue} {
		if !strings.Contains(page, want) {
			t.Errorf("telemetry page does not mention %q", want)
		}
	}
}
