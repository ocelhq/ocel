package host

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestAContainerIsBuiltForTheHostUnlessItsAppDeclaresAnArchitectureTheHostDoesNotRun(t *testing.T) {
	t.Parallel()

	for _, declared := range []string{"", "arm64"} {
		runs, err := ContainerArch("web", declared, ssh.ARM64)
		if err != nil || runs != ssh.ARM64 {
			t.Errorf("ContainerArch(%q) on an arm64 host = %q, %v, want arm64: the image runs on this host and nowhere else", declared, runs, err)
		}
	}
	_, err := ContainerArch("web", "x86_64", ssh.ARM64)
	if err == nil {
		t.Fatal("ContainerArch(x86_64) on an arm64 host named an architecture, and the image would be built for a machine the host is not")
	}
	for _, named := range []string{"web", "x86_64", ssh.ARM64} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("the refusal reads %q and never names %q", err, named)
		}
	}
}
