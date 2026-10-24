//go:build integration

package host

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/enginetest"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

func TestAProbeStartsWhereAnInterruptedRunLeftItsContainerBehind(t *testing.T) {
	engineOrFail(t)

	run := append([]string{"run", "--detach", "--name", probeName(t)}, enginetest.RunLabelArgs(t)...)
	if out, err := exec.Command(dockerEngine, append(run,
		"--network", enginetest.Network(t, "box"), "--entrypoint", "sleep", caddy.Image(), "600")...).CombinedOutput(); err != nil {
		t.Fatalf("plant the container a killed run would have left under the name this probe takes: %v\n%s", err, out)
	}

	aLiveProxy(t)
}

func TestTheProxyAndSwitchboardAProbeStartsCarryItsRunsLabel(t *testing.T) {
	proxy := aLiveProxy(t)

	label, run, _ := strings.Cut(enginetest.RunLabelArgs(t)[1], "=")
	for _, container := range []string{proxy.name, proxy.board} {
		said, err := exec.Command(dockerEngine, "inspect", "--format", `{{index .Config.Labels "`+label+`"}}`, container).Output()
		if err != nil {
			t.Fatalf("inspect %s: %v", container, err)
		}
		if got := strings.TrimSpace(string(said)); got != run {
			t.Errorf("%s carries %s=%q, want %q: a run killed mid-test leaves it publishing the box's ports, and the next run to take them cannot tell it is a leftover", container, label, got, run)
		}
	}
}
