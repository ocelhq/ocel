package buildoutput_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestANextAppIsPresumedBeforeItsBuildToUseRevalidationAndImageOptimization(t *testing.T) {
	t.Parallel()

	want := buildoutput.Uses{Revalidation: true, ImageOptimization: true}
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
