package host

import (
	"os/exec"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/enginetest"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

func TestAProbeStartsWhereAnInterruptedRunLeftItsContainerBehind(t *testing.T) {
	engineOrSkip(t)

	if out, err := exec.Command(dockerEngine, "run", "--detach", "--name", probeName(t),
		"--network", enginetest.Network(t), "--entrypoint", "sleep", caddy.Image, "600").CombinedOutput(); err != nil {
		t.Fatalf("plant the container a killed run would have left under the name this probe takes: %v\n%s", err, out)
	}

	aLiveProxy(t)
}
