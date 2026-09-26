package appbuild_test

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	kit  = "github.com/ocelhq/ocel/pkg/providerkit"
	self = kit + "/appbuild"
)

var wire = []string{
	"github.com/ocelhq/ocel/pkg/proto",
	"connectrpc.com/connect",
	"google.golang.org/protobuf",
	"buf.build/",
}

func TestARuntimeBinaryReadsTheAppBuildWithoutLinkingTheWireOrTheKit(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if pkg != self && (pkg == kit || strings.HasPrefix(pkg, kit+"/")) {
			t.Errorf("reading the app build reaches %s: a runtime binary that only reads what the build wrote is a leaf and must link nothing else of the kit", pkg)
		}
		for _, linked := range wire {
			if strings.HasPrefix(pkg, linked) {
				t.Errorf("reading the app build reaches %s: a runtime binary that only reads what the build wrote must not link the wire", pkg)
			}
		}
	}
}
