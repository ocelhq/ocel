package buildoutput_test

import (
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestANextAppIsPresumedBeforeItsBuildToUseISRAndImageOptimization(t *testing.T) {
	t.Parallel()

	want := buildoutput.Uses{ISR: true, ImageOptimization: true}
	if got := buildoutput.PresumeUses(buildoutput.FrameworkNext); got != want {
		t.Errorf("PresumeUses(next) = %+v, want %+v", got, want)
	}
}

func TestEveryOtherFrameworkIsPresumedBeforeItsBuildToUseNothing(t *testing.T) {
	t.Parallel()

	for _, name := range append([]string{"astro"}, buildoutput.Frameworks()...) {
		if name == buildoutput.FrameworkNext {
			continue
		}
		if got := buildoutput.PresumeUses(name); got != (buildoutput.Uses{}) {
			t.Errorf("PresumeUses(%q) = %+v, want nothing", name, got)
		}
	}
}

func TestUsesHasAnyOfAnotherOnlyWhenTheyShareOne(t *testing.T) {
	t.Parallel()

	isr := buildoutput.Uses{ISR: true}
	images := buildoutput.Uses{ImageOptimization: true}
	both := buildoutput.Uses{ISR: true, ImageOptimization: true}
	for _, tc := range []struct {
		uses, other buildoutput.Uses
		want        bool
	}{
		{isr, both, true},
		{both, images, true},
		{isr, images, false},
		{buildoutput.Uses{}, both, false},
	} {
		if got := tc.uses.HasAnyOf(tc.other); got != tc.want {
			t.Errorf("%+v.HasAnyOf(%+v) = %v, want %v", tc.uses, tc.other, got, tc.want)
		}
	}
}

func TestHostingDeclaresISROnlyForABuildThatUsesIt(t *testing.T) {
	t.Parallel()

	var declared buildoutput.Hosting
	if err := json.Unmarshal([]byte(`{"version":1,"framework":"sveltekit","isr":true,"needs":{}}`), &declared); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !declared.ISR {
		t.Errorf("hosting = %+v, want the ISR it declares", declared)
	}
	encoded, err := json.Marshal(buildoutput.Hosting{Version: buildoutput.HostingVersion, Framework: "node"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := keys["isr"]; ok {
		t.Errorf("hosting.json = %s, want no isr key for a build that does not use it", encoded)
	}
}
