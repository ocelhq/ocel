package gcp

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
)

func TestAContainerBuiltFromAFunctionThatIsNotNextIsRefused(t *testing.T) {
	runtime := newProvider(&control.Provider{}).Runtime()
	node := buildoutput.Framework{Name: buildoutput.FrameworkNode}

	if _, err := runtime.FunctionBase(context.Background(), node); err == nil || !strings.Contains(err.Error(), "Dockerfile") {
		t.Errorf("FunctionBase(node) = %v, want a refusal naming the Dockerfile that builds the container instead", err)
	}
	if _, err := runtime.FunctionFiles(context.Background(), node); err == nil || !strings.Contains(err.Error(), "Dockerfile") {
		t.Errorf("FunctionFiles(node) = %v, want a refusal naming the Dockerfile that builds the container instead", err)
	}
}

func TestAContainerBuiltFromAFunctionCarriesNoBuildFileSinceItsStaticFilesAndPrerendersAreUploaded(t *testing.T) {
	if paths := newProvider(&control.Provider{}).Runtime().FunctionBuildPaths(buildoutput.Framework{Name: buildoutput.FrameworkNext, Arch: "x86_64"}); len(paths) != 0 {
		t.Errorf("FunctionBuildPaths() = %v, want none: the runtime reads them from Cloud Storage, and an image carrying them too pulls slower", paths)
	}
}
