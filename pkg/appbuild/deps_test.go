package appbuild_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const repo = "github.com/ocelhq/ocel/"

var reachable = []string{
	repo + "pkg/appbuild",
	repo + "pkg/constants",
	repo + "platform/edge/contract",
}

var wire = []string{
	"github.com/ocelhq/ocel/pkg/proto",
	"connectrpc.com/connect",
	"google.golang.org/protobuf",
	"buf.build/",
}

func TestARuntimeBinaryReadsTheAppBuildLinkingOnlyTheConstantsAndTheEdgeContractOfOcel(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, repo) && !slices.Contains(reachable, pkg) {
			t.Errorf("reading the app build reaches %s: a runtime binary that only reads what the build wrote is a leaf and must link nothing else of ocel", pkg)
		}
		for _, linked := range wire {
			if strings.HasPrefix(pkg, linked) {
				t.Errorf("reading the app build reaches %s: a runtime binary that only reads what the build wrote must not link the wire", pkg)
			}
		}
	}
}
