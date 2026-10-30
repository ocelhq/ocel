package host

import (
	"slices"
	"strconv"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

func TestEveryContainerOcelRunsForTheBoxItselfRunsUnderAMemoryLimit(t *testing.T) {
	t.Parallel()

	for name, box := range map[string]docker.Service{
		"the switchboard": switchboardBox(switchboardBinary(testAssets, ssh.AMD64), front.Proxy{}),
		"the proxy":       frontProxy(),
		"the tunnel":      tunnel.Service(boxapi.Tunnel{Name: "ocel-box", ID: "5a6b7c8d-1"}, edge.TunnelConnector{Image: "example/connector@sha256:abc", TokenFileEnv: "TOKEN_FILE"}),
	} {
		for what, argv := range map[string][]string{"run": box.Run(), "create": box.Create()} {
			at := slices.Index(argv, "--memory")
			if at < 0 || at+1 >= len(argv) {
				t.Errorf("%s is %s with no --memory: %v, and a box container nothing bounds can take the memory of every app on the box", name, what, argv)
				continue
			}
			if limit, err := strconv.ParseInt(argv[at+1], 10, 64); err != nil || limit < docker.MinimumMemoryBytes {
				t.Errorf("%s is %s under --memory %q, want at least %d bytes", name, what, argv[at+1], docker.MinimumMemoryBytes)
			}
		}
		if !slices.Contains(slicesOfLines(box.Facts()), "memory="+strconv.FormatInt(box.Memory, 10)) {
			t.Errorf("%s states no memory among its facts, so a container started without the limit surveys as current", name)
		}
	}
}

func slicesOfLines(facts []byte) []string {
	var lines []string
	start := 0
	for at, b := range facts {
		if b == '\n' {
			lines = append(lines, string(facts[start:at]))
			start = at + 1
		}
	}
	return lines
}
