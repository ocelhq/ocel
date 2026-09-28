package boxstore_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
)

func TestTheLocalTransportRunsTheSealHelperUnderItsElevationAndBareWithoutOne(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		elevation []string
		want      string
	}{
		{name: "bare", want: "seal production open\n"},
		{name: "elevated", elevation: []string{"echo", "sudo", "-n"}, want: "sudo -n echo seal production open\n"},
	} {
		said, err := boxstore.LocalTransport{Elevation: tc.elevation}.Seal(context.Background(), "open a value",
			environment.TierProduction, []string{"echo", "seal", "production", "open"}, nil)
		if err != nil {
			t.Fatalf("%s: Seal() = %v", tc.name, err)
		}
		if said != tc.want {
			t.Errorf("%s: the transport ran what printed %q, want %q", tc.name, said, tc.want)
		}
	}
}

func TestABinaryInstalledOnTheBoxLinksTheBoxStoreWithoutEmbeddingItself(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if pkg == "github.com/ocelhq/ocel/platform/vps/provider/host" {
			t.Errorf("the box store reaches %s, which embeds every binary bootstrap installs, so a binary on the box linking it would embed itself", pkg)
		}
	}
}
