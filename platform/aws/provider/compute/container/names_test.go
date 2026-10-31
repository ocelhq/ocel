package container

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/containerservice"
)

func TestAContainersTargetGroupIsNamedUnderTheAppFenceByTheHashOfItsRelease(t *testing.T) {
	t.Parallel()

	stack := naming.AppStack("prod", "web", fixedRelease(t))

	if got, want := TargetGroupName("ocel", containerservice.Coordinate("shop", stack)), "ocel-app-tg-488394"; got != want {
		t.Errorf("TargetGroupName = %q, want %q", got, want)
	}
	next := naming.AppStack("prod", "web", naming.NewReleaseToken("build-2", "fingerprint"))
	if TargetGroupName("ocel", containerservice.Coordinate("shop", next)) == TargetGroupName("ocel", containerservice.Coordinate("shop", stack)) {
		t.Error("two releases of one app share a target group name")
	}
}
