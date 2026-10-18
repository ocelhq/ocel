package host

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/envfile"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

func TestEveryContainerThisHostStartsPullsItsImageAheadOfRunningIt(t *testing.T) {
	t.Parallel()

	app, resource := valued(), resourced()
	for what, container := range map[string]struct {
		image   string
		command string
	}{
		"an app container":     {app.Image, release.RunContainerScript(app, handedTo(app))},
		"a resource container": {resource.Image, container.RunScript(resource, "0123456789ab", envfile.Path(resource.Tier, resource.Name))},
	} {
		pull := strings.Index(container.command, "docker pull "+shell.Quote(container.image))
		run := strings.Index(container.command, shell.Quote("--name"))
		if pull < 0 || run < 0 || pull > run {
			t.Fatalf("%s is run without an explicit pull ahead of it, so a registry read reset mid-layer is `docker run`'s one unretried attempt:\n%s", what, container.command)
		}
		for _, want := range []string{
			"docker image inspect " + shell.Quote(container.image),
			fmt.Sprintf("-ge %d", docker.PullAttempts),
		} {
			if !strings.Contains(container.command, want) {
				t.Errorf("%s contains no %q, so its pull is either unbounded or repeated on a box that already has the image:\n%s", what, want, container.command)
			}
		}
		if !strings.Contains(container.command, docker.ScriptPullBackoff.Start()) || !strings.Contains(container.command, docker.ScriptPullBackoff.Again()) {
			t.Errorf("%s backs off between pulls in a spelling of its own rather than scriptPullBackoff, so a fleet retrying at once is neither spread out nor capped:\n%s", what, container.command)
		}
	}
}

func TestTheWaitBetweenTriesBacksOffToACeilingAndIsSpreadOut(t *testing.T) {
	t.Parallel()

	for what, backoff := range map[string]shell.Backoff{"a pull": docker.ScriptPullBackoff} {
		again := backoff.Again()
		for _, want := range []string{"backoff=$((backoff * 2))", "sleep $((backoff + jitter))", fmt.Sprintf("backoff=%d; fi", backoff.Ceiling)} {
			if !strings.Contains(again, want) {
				t.Errorf("the wait before %s retries contains no %q:\n%s", what, want, again)
			}
		}
		if !strings.Contains(backoff.Start(), fmt.Sprintf("backoff=%d", backoff.Base)) {
			t.Errorf("the wait before %s retries never opens on its base of %d:\n%s", what, backoff.Base, backoff.Start())
		}
	}
}
