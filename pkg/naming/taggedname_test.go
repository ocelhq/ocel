package naming_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestTaggedNameJoinsItsHeadReadablePartAndTag(t *testing.T) {
	got := naming.TaggedName{Head: "ocel-cert", Readable: "shop-example-com", Tag: "1a2b3c"}.Fit(63, "auth")
	if want := "ocel-cert-shop-example-com-1a2b3c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestTaggedNameCutsOnlyItsReadablePartLeavingRoomForTheLongestChild(t *testing.T) {
	name := naming.TaggedName{Head: "ocel-prod-alb", Readable: "binding-" + strings.Repeat("a", 60), Tag: "1a2b3c"}
	got := name.Fit(63, "cert", "neg")
	if want := "ocel-prod-alb-binding-" + strings.Repeat("a", 29) + "-1a2b3c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
	if child := got + "-cert"; len(child) != 63 {
		t.Errorf("child %q is %d characters, want exactly the limit of 63", child, len(child))
	}
}

func TestTaggedNameTrimsTheDashACutLeavesBehind(t *testing.T) {
	got := naming.TaggedName{Head: "ocel-cert", Readable: strings.Repeat("a", 22) + "-" + strings.Repeat("b", 30), Tag: "1a2b3c"}.Fit(40)
	if want := "ocel-cert-" + strings.Repeat("a", 22) + "-1a2b3c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestTaggedNameKeepsAnUnderscoreInItsHead(t *testing.T) {
	got := naming.TaggedName{Head: "ocel_gate-prod", Readable: "shop", Tag: "1a2b3c"}.Fit(207)
	if want := "ocel_gate-prod-shop-1a2b3c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}
