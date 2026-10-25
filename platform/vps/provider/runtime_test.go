package vps

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestAContainerBuiltFromAFunctionThatIsNotNextIsRefused(t *testing.T) {
	runtime := (&Provider{}).Runtime()
	node := buildoutput.Framework{Name: buildoutput.FrameworkNode}

	if _, err := runtime.FunctionBase(context.Background(), node); err == nil || !strings.Contains(err.Error(), "Dockerfile") {
		t.Errorf("FunctionBase(node) = %v, want a refusal naming the Dockerfile that builds the container instead", err)
	}
	if _, err := runtime.FunctionFiles(context.Background(), node); err == nil || !strings.Contains(err.Error(), "Dockerfile") {
		t.Errorf("FunctionFiles(node) = %v, want a refusal naming the Dockerfile that builds the container instead", err)
	}
}

func TestAContainerBuiltFromAFunctionCarriesWhatItsRuntimeReadsFromItsOwnDisk(t *testing.T) {
	paths := (&Provider{}).Runtime().FunctionBuildPaths(buildoutput.Framework{Name: buildoutput.FrameworkNext, Arch: "x86_64"})
	slices.Sort(paths)
	if want := []string{"image-config.json", "static"}; !slices.Equal(paths, want) {
		t.Errorf("FunctionBuildPaths() = %v, want %v: a box has no bucket, so the static files and image config its optimizer reads ride in the image, and Next's own cache reads no seed set", paths, want)
	}
}
