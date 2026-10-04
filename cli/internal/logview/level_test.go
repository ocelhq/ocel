package logview_test

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/logview"
)

func TestParseLevelAcceptsTheFourLevelNamesInAnyCase(t *testing.T) {
	t.Parallel()

	cases := map[string]logview.Level{
		"debug": logview.LevelDebug,
		"INFO":  logview.LevelInfo,
		"Warn":  logview.LevelWarn,
		"error": logview.LevelError,
	}
	for name, want := range cases {
		got, ok := logview.ParseLevel(name)
		if !ok || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v, want %v, true", name, got, ok, want)
		}
	}
}

func TestParseLevelRefusesAnyOtherName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "trace", "warning", "fatal", "30"} {
		if got, ok := logview.ParseLevel(name); ok {
			t.Errorf("ParseLevel(%q) = %v, true, want it refused", name, got)
		}
	}
}

func TestLevelStringNamesEachLevelInLowerCase(t *testing.T) {
	t.Parallel()

	cases := map[logview.Level]string{
		logview.LevelUnknown: "",
		logview.LevelDebug:   "debug",
		logview.LevelInfo:    "info",
		logview.LevelWarn:    "warn",
		logview.LevelError:   "error",
	}
	for level, want := range cases {
		if got := level.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", level, got, want)
		}
	}
}

func TestResolveLevelPrefersTheLevelParsedFromTheMessageOverTheVendorSeverity(t *testing.T) {
	t.Parallel()

	if got := logview.ResolveLevel(logview.LevelInfo, "ERROR", false); got != logview.LevelInfo {
		t.Errorf("ResolveLevel(info, ERROR) = %v, want info", got)
	}
}

func TestResolveLevelFallsBackToTheVendorSeverityWhenTheMessageHasNoLevel(t *testing.T) {
	t.Parallel()

	cases := map[string]logview.Level{
		"WARNING":   logview.LevelWarn,
		"NOTICE":    logview.LevelInfo,
		"CRITICAL":  logview.LevelError,
		"debug":     logview.LevelDebug,
		"DEFAULT":   logview.LevelUnknown,
		"":          logview.LevelUnknown,
		"EMERGENCY": logview.LevelError,
	}
	for severity, want := range cases {
		if got := logview.ResolveLevel(logview.LevelUnknown, severity, false); got != want {
			t.Errorf("ResolveLevel(unknown, %q) = %v, want %v", severity, got, want)
		}
	}
}

func TestResolveLevelPutsAFailureAtErrorWhateverElseItSays(t *testing.T) {
	t.Parallel()

	for _, parsed := range []logview.Level{logview.LevelUnknown, logview.LevelDebug, logview.LevelInfo, logview.LevelError} {
		if got := logview.ResolveLevel(parsed, "INFO", true); got != logview.LevelError {
			t.Errorf("ResolveLevel(%v, INFO, failure) = %v, want error", parsed, got)
		}
	}
}
