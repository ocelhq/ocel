package host

import (
	"fmt"
	"strings"
	"testing"
)

func TestEveryContainerThisHostStartsPullsItsImageAheadOfRunningIt(t *testing.T) {
	t.Parallel()

	app, resource := valued(), resourced()
	for what, container := range map[string]struct {
		image   string
		command string
	}{
		"an app container":     {app.Image, runContainerScript(app, handedTo(app))},
		"a resource container": {resource.Image, runResourceScript(resource, "0123456789ab", EnvFile(resource.Class, resource.Name))},
	} {
		pull := strings.Index(container.command, "docker pull "+quoted(container.image))
		run := strings.Index(container.command, quoted("--name"))
		if pull < 0 || run < 0 || pull > run {
			t.Fatalf("%s is run without an explicit pull ahead of it, so a registry read reset mid-layer is `docker run`'s one unretried attempt:\n%s", what, container.command)
		}
		for _, want := range []string{
			"docker image inspect " + quoted(container.image),
			fmt.Sprintf("-ge %d", appPulls),
		} {
			if !strings.Contains(container.command, want) {
				t.Errorf("%s contains no %q, so its pull is either unbounded or repeated on a box that already has the image:\n%s", what, want, container.command)
			}
		}
		if !strings.Contains(container.command, scriptPullBackoff.start()) || !strings.Contains(container.command, scriptPullBackoff.again()) {
			t.Errorf("%s backs off between pulls in a spelling of its own rather than scriptPullBackoff, so a fleet retrying at once is neither spread out nor capped:\n%s", what, container.command)
		}
	}
}

func TestTheWaitBetweenTriesBacksOffToACeilingAndIsSpreadOut(t *testing.T) {
	t.Parallel()

	for what, backoff := range map[string]retryBackoff{"a pull": scriptPullBackoff} {
		again := backoff.again()
		for _, want := range []string{"backoff=$((backoff * 2))", "sleep $((backoff + jitter))", fmt.Sprintf("backoff=%d; fi", backoff.ceiling)} {
			if !strings.Contains(again, want) {
				t.Errorf("the wait before %s retries contains no %q:\n%s", what, want, again)
			}
		}
		if !strings.Contains(backoff.start(), fmt.Sprintf("backoff=%d", backoff.base)) {
			t.Errorf("the wait before %s retries never opens on its base of %d:\n%s", what, backoff.base, backoff.start())
		}
	}
}
