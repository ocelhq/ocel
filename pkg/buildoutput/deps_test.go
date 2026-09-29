package buildoutput_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const repo = "github.com/ocelhq/ocel/"

var reachable = []string{
	repo + "pkg/buildoutput",
	repo + "pkg/containerimage",
	repo + "pkg/edge",
	repo + "pkg/environment",
	repo + "pkg/processenv",
	repo + "pkg/progress",
	repo + "pkg/router",
	repo + "pkg/statedir",
}

var protocol = []string{
	"github.com/ocelhq/ocel/pkg/proto",
	"connectrpc.com/connect",
	"google.golang.org/protobuf",
	"buf.build/",
}

func TestARuntimeBinaryReadsTheBuildOutputLinkingOnlyItsNamesAndTheEdgeContractOfOcel(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".", "../containerimage", "../processenv").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, repo) && !slices.Contains(reachable, pkg) {
			t.Errorf("reading the build output reaches %s: a runtime binary that only reads what the build wrote is a leaf and must link nothing else of ocel", pkg)
		}
		for _, linked := range protocol {
			if strings.HasPrefix(pkg, linked) {
				t.Errorf("reading the build output reaches %s: a runtime binary that only reads what the build wrote must not link protobuf or connect", pkg)
			}
		}
	}
}
