package conformance

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type runtimeWithoutEntrypoint struct{ provider.Runtime }

func (runtimeWithoutEntrypoint) FunctionFiles(context.Context, buildoutput.Framework) (map[string][]byte, error) {
	return map[string][]byte{"server.mjs": []byte("export {}")}, nil
}

func TestARuntimeThatShipsNoEntrypointForANextFunctionIsAFault(t *testing.T) {
	runtime := runtimeWithoutEntrypoint{fake.NewProvider(fake.Options{}).Runtime()}

	faults := findFunctionContainerFaults(context.Background(), runtime, stagedNextFunction(t))
	if len(faults) == 0 || !strings.Contains(faults[0], "has no entrypoint.mjs") {
		t.Errorf("faults = %q, want each naming the missing entrypoint", faults)
	}
}

func TestTheReferenceRuntimeBuildsAContainerFromANextFunction(t *testing.T) {
	if faults := findFunctionContainerFaults(context.Background(), fake.NewProvider(fake.Options{}).Runtime(), stagedNextFunction(t)); len(faults) != 0 {
		t.Errorf("faults = %q, want none", faults)
	}
}
