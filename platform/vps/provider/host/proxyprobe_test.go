//go:build integration

package host

import (
	"os/exec"
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
