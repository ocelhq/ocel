package host

import (
	"fmt"
	"strings"
	"testing"
)

func TestEveryContainerThisHostStandsUpPullsItsImageAheadOfRunningIt(t *testing.T) {
	t.Parallel()

	app, resource := valued(), resourced()
	for what, stood := range map[string]struct {
		image   string
		command string
	}{
		"an app container":     {app.Image, containerStanding(app, handedTo(app))},
		"a resource container": {resource.Image, resourceStanding(resource, "0123456789ab", EnvFile(resource.Class, resource.Name))},
	} {
		pull := strings.Index(stood.command, "docker pull "+quoted(stood.image))
		run := strings.Index(stood.command, quoted("--name"))
		if pull < 0 || run < 0 || pull > run {
			t.Fatalf("%s is run without an explicit pull ahead of it, so a registry read reset mid-layer is `docker run`'s one unretried attempt:\n%s", what, stood.command)
		}
		for _, want := range []string{
			"docker image inspect " + quoted(stood.image),
			fmt.Sprintf("-ge %d", appPulls),
		} {
			if !strings.Contains(stood.command, want) {
				t.Errorf("%s carries no %q, so its pull is either unbounded or repeated on a box that already holds the image:\n%s", what, want, stood.command)
			}
		}
		if !strings.Contains(stood.command, pullHold.start()) || !strings.Contains(stood.command, pullHold.again()) {
			t.Errorf("%s holds off between pulls in a spelling of its own rather than the one the engine install already uses, so a fleet retrying at once is neither spread out nor capped:\n%s", what, stood.command)
		}
	}
}

func TestTheHoldBetweenTriesBacksOffToACeilingAndIsSpreadOut(t *testing.T) {
	t.Parallel()

	for what, held := range map[string]hold{"a pull": pullHold, "the engine install": engineInstallHold} {
		again := held.again()
		for _, want := range []string{"backoff=$((backoff * 2))", "sleep $((backoff + jitter))", fmt.Sprintf("backoff=%d; fi", held.ceiling)} {
			if !strings.Contains(again, want) {
				t.Errorf("the hold before %s retries carries no %q:\n%s", what, want, again)
			}
		}
		if !strings.Contains(held.start(), fmt.Sprintf("backoff=%d", held.base)) {
			t.Errorf("the hold before %s retries never opens on its base of %d:\n%s", what, held.base, held.start())
		}
	}
}
