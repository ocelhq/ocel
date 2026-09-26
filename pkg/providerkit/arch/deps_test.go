package arch_test

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	kit  = "github.com/ocelhq/ocel/pkg/providerkit"
	self = kit + "/arch"
)

var wire = []string{
	"github.com/ocelhq/ocel/pkg/proto",
	"connectrpc.com/connect",
	"google.golang.org/protobuf",
	"buf.build/",
}

func TestNamingAnArchitectureLinksNeitherTheWireNorTheRestOfTheKit(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if pkg != self && (pkg == kit || strings.HasPrefix(pkg, kit+"/")) {
			t.Errorf("naming an architecture reaches %s: the project config and every packager read it, so it is a leaf and must link nothing else of the kit", pkg)
		}
		for _, linked := range wire {
			if strings.HasPrefix(pkg, linked) {
				t.Errorf("naming an architecture reaches %s: the project config and every packager read it, and must not link the wire for it", pkg)
			}
		}
	}
}
