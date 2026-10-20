package container

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestAContainersTargetGroupIsNamedUnderTheAppFenceByTheHashOfItsRelease(t *testing.T) {
	t.Parallel()

	stack := naming.AppStack("prod", "web", fixedRelease(t))

	if got, want := TargetGroupName("ocel", serviceCoordinate("shop", stack)), "ocel-app-tg-488394"; got != want {
		t.Errorf("TargetGroupName = %q, want %q", got, want)
	}
	next := naming.AppStack("prod", "web", naming.NewReleaseToken("build-2", "fingerprint"))
	if TargetGroupName("ocel", serviceCoordinate("shop", next)) == TargetGroupName("ocel", serviceCoordinate("shop", stack)) {
		t.Error("two releases of one app share a target group name")
	}
}
