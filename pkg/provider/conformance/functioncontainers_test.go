package conformance

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type runtimeWithoutEntrypoint struct{ provider.Runtime }

func (runtimeWithoutEntrypoint) FunctionFiles(context.Context, buildoutput.Framework) (map[string][]byte, error) {
	return map[string][]byte{"server.mjs": []byte("export {}")}, nil
}

type runtimeOnABaseWithoutNode struct{ provider.Runtime }

func (runtimeOnABaseWithoutNode) FunctionBase(context.Context, buildoutput.Framework) (v1.Image, error) {
	return empty.Image, nil
}

func TestARuntimeThatShipsNoEntrypointForANextFunctionIsAFault(t *testing.T) {
	runtime := runtimeWithoutEntrypoint{fake.NewProvider(fake.Options{}).Runtime()}

	faults := findFunctionContainerFaults(context.Background(), runtime, stagedNextFunction(t))
	if len(faults) == 0 || !strings.Contains(faults[0], "has no entrypoint.mjs") {
		t.Errorf("faults = %q, want each naming the missing entrypoint", faults)
	}
}

func TestARuntimeWhoseBaseCannotRunItsFunctionsCommandIsAFault(t *testing.T) {
	runtime := runtimeOnABaseWithoutNode{fake.NewProvider(fake.Options{}).Runtime()}

	faults := findFunctionContainerFaults(context.Background(), runtime, stagedNextFunction(t))
	if len(faults) == 0 || !strings.Contains(faults[0], "node") {
		t.Errorf("faults = %q, want each naming the node its base does not put on the path", faults)
	}
}

func TestTheReferenceRuntimeBuildsAContainerFromANextFunction(t *testing.T) {
	if faults := findFunctionContainerFaults(context.Background(), fake.NewProvider(fake.Options{}).Runtime(), stagedNextFunction(t)); len(faults) != 0 {
		t.Errorf("faults = %q, want none", faults)
	}
}
