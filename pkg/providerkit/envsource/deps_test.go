package envsource_test

import (
	"os/exec"
	"strings"
	"testing"
)

var wire = []string{
	"github.com/ocelhq/ocel/pkg/proto",
	"connectrpc.com/connect",
	"google.golang.org/protobuf",
	"buf.build/",
}

func TestATargetSideSyncerReadsSourcesWithoutLinkingTheWire(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		for _, linked := range wire {
			if strings.HasPrefix(pkg, linked) {
				t.Errorf("reading a source reaches %s: the syncer a target runs must not link the wire", pkg)
			}
		}
	}
}
