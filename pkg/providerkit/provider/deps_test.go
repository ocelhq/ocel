package provider_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

var imageMachinery = []string{
	"github.com/ocelhq/ocel/pkg/providerkit/images",
	"github.com/docker/",
	"github.com/google/go-containerregistry/",
}

var imageContract = []string{
	"github.com/google/go-containerregistry/pkg/v1",
	"github.com/google/go-containerregistry/pkg/v1/types",
}

func TestTheContractNamesAnImageWithoutLinkingWhatBuildsOrPushesIt(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if slices.Contains(imageContract, pkg) {
			continue
		}
		for _, linked := range imageMachinery {
			if strings.HasPrefix(pkg, linked) {
				t.Errorf("the contract reaches %s: every vendor, connector and runtime binary that implements it would link what builds and pushes images", pkg)
			}
		}
	}
}
