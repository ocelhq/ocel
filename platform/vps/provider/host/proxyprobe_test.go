package host

import (
	"os/exec"
	"testing"
)

func TestAProbeStandsWhereAnInterruptedRunLeftItsNetworkAndContainerBehind(t *testing.T) {
	engineOrSkip(t)

	name, network := probeName(t), probeName(t)+"-net"
	if out, err := exec.Command(dockerEngine, "network", "create", network).CombinedOutput(); err != nil {
		t.Fatalf("plant the network a killed run would have left: %v\n%s", err, out)
	}
	if out, err := exec.Command(dockerEngine, "run", "--detach", "--name", name,
		"--network", network, "--entrypoint", "sleep", ProxyImage, "600").CombinedOutput(); err != nil {
		t.Fatalf("plant the container a killed run would have left on that network: %v\n%s", err, out)
	}

	proxyStanding(t)
}
