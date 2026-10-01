package image

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/log"
)

func TestWhatRailpackLogsDuringABuildLandsInThatBuildsProgressRatherThanTheTerminal(t *testing.T) {
	var progress bytes.Buffer
	restore := railpackLogsTo(&progress)
	log.Warnf("could not detect pnpm lockfile version")
	log.Infof("frontend cache imports: %v", []string{})
	restore()

	if got, want := progress.String(), "WARN could not detect pnpm lockfile version\nINFO frontend cache imports: []\n"; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}

	log.Warnf("after the build")
	if got := progress.String(); bytes.Contains([]byte(got), []byte("after the build")) {
		t.Fatalf("progress = %q, want nothing railpack logs after the build to reach it", got)
	}
}
