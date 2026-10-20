package naming_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func fitTagged(t *testing.T, name naming.TaggedName, limit int, childSuffixes ...string) string {
	t.Helper()
	got, err := name.Fit(limit, childSuffixes...)
	if err != nil {
		t.Fatalf("Fit(%d) = %v", limit, err)
	}
	return got
}

func TestTaggedNameJoinsItsHeadReadablePartAndTag(t *testing.T) {
	got := fitTagged(t, naming.TaggedName{Head: "ocel-cert", Readable: "shop-example-com", Tag: "1a2b3c"}, 63, "auth")
	if want := "ocel-cert-shop-example-com-1a2b3c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestTaggedNameCutsOnlyItsReadablePartLeavingRoomForTheLongestChild(t *testing.T) {
	name := naming.TaggedName{Head: "ocel-prod-alb", Readable: "binding-" + strings.Repeat("a", 60), Tag: "1a2b3c"}
	got := fitTagged(t, name, 63, "cert", "neg")
	if want := "ocel-prod-alb-binding-" + strings.Repeat("a", 29) + "-1a2b3c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
	if child := got + "-cert"; len(child) != 63 {
		t.Errorf("child %q is %d characters, want exactly the limit of 63", child, len(child))
	}
}

func TestTaggedNameTrimsTheDashACutLeavesBehind(t *testing.T) {
	got := fitTagged(t, naming.TaggedName{Head: "ocel-cert", Readable: strings.Repeat("a", 22) + "-" + strings.Repeat("b", 30), Tag: "1a2b3c"}, 40)
	if want := "ocel-cert-" + strings.Repeat("a", 22) + "-1a2b3c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestTaggedNameKeepsAnUnderscoreInItsHead(t *testing.T) {
	got := fitTagged(t, naming.TaggedName{Head: "ocel_gate-prod", Readable: "shop", Tag: "1a2b3c"}, 207)
	if want := "ocel_gate-prod-shop-1a2b3c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestATaggedNameFittedWholeKeepsAllOfItsReadablePartUpToTheLimit(t *testing.T) {
	name := naming.TaggedName{Head: "ocel-prod", Readable: strings.Repeat("s", 20), Tag: "1a2b3c"}
	got, err := name.FitWhole(41, "dlq")
	if want := "ocel-prod-" + strings.Repeat("s", 20) + "-1a2b3c"; err != nil || got != want {
		t.Errorf("FitWhole(41) = %q, %v, want %q", got, err, want)
	}
}

func TestATaggedNameThatDoesNotFitWholeIsRefusedRatherThanCut(t *testing.T) {
	var refused refusal.Refusal
	for name, c := range map[string]struct {
		limit    int
		children []string
	}{
		"over the limit":            {limit: 36},
		"its longest child over it": {limit: 40, children: []string{"x", "dlq"}},
	} {
		got, err := naming.TaggedName{Head: "ocel-prod", Readable: strings.Repeat("s", 20), Tag: "1a2b3c"}.FitWhole(c.limit, c.children...)
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("%s: FitWhole(%d) = %q, %v, want an invalid refusal", name, c.limit, got, err)
		}
	}
}

func TestTaggedNameWhoseHeadAndTagLeaveNoRoomForItsChildrenIsRefused(t *testing.T) {
	var refused refusal.Refusal
	for name, c := range map[string]struct {
		limit    int
		children []string
	}{
		"over the limit":            {limit: 20},
		"its longest child over it": {limit: 30, children: []string{"x", "certificate"}},
	} {
		got, err := naming.TaggedName{Head: "ocel-prod-edge-cert", Readable: "shop", Tag: "1a2b3c"}.Fit(c.limit, c.children...)
		if !errors.As(err, &refused) {
			t.Errorf("%s: Fit(%d) = %q, %v, want a refusal", name, c.limit, got, err)
		}
	}
}
