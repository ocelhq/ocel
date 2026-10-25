package functioncode_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
)

func TestAContainerIsBuiltFromANextFunctionAndAnyOtherIsRefusedForWantOfADockerfile(t *testing.T) {
	if err := functioncode.RefuseContainerFramework(buildoutput.Framework{Name: buildoutput.FrameworkNext}); err != nil {
		t.Errorf("RefuseContainerFramework(next) = %v, want nil", err)
	}
	err := functioncode.RefuseContainerFramework(buildoutput.Framework{Name: buildoutput.FrameworkNode})
	if want := "a node container has no Dockerfile: add one"; err == nil || err.Error() != want {
		t.Errorf("RefuseContainerFramework(node) = %v, want %q", err, want)
	}
}
