package buildoutput_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestPythonIsARuntimeAnAppMayDeclare(t *testing.T) {
	t.Parallel()

	if !buildoutput.IsKnownFramework(buildoutput.FrameworkPython) {
		t.Fatalf("Frameworks() = %v, and none of them is %q", buildoutput.Frameworks(), buildoutput.FrameworkPython)
	}
}

func TestRustIsARuntimeAnAppMayDeclare(t *testing.T) {
	t.Parallel()

	if !buildoutput.IsKnownFramework(buildoutput.FrameworkRust) {
		t.Fatalf("Frameworks() = %v, and none of them is %q", buildoutput.Frameworks(), buildoutput.FrameworkRust)
	}
}

func TestTheArchitecturesAreTheOnesEveryRuntimeSharesAVocabularyFor(t *testing.T) {
	t.Parallel()

	for _, architecture := range []string{arch.X8664, arch.ARM64} {
		if _, known := arch.GoArch(architecture); !known {
			t.Errorf("GoArch(%q) names nothing go builds for", architecture)
		}
		if _, known := arch.PythonPlatformTag(architecture); !known {
			t.Errorf("PythonPlatformTag(%q) names no wheel platform", architecture)
		}
	}
	if !slices.Contains(buildoutput.Frameworks(), buildoutput.FrameworkGo) {
		t.Error("Frameworks() no longer names the go framework")
	}
}

func TestSvelteKitIsAFrameworkThatRunsOnNodeAndBuildsWithItsOwnScript(t *testing.T) {
	t.Parallel()

	if !buildoutput.IsKnownFramework(buildoutput.FrameworkSvelteKit) {
		t.Fatalf("Frameworks() = %v, and none of them is %q", buildoutput.Frameworks(), buildoutput.FrameworkSvelteKit)
	}
	if !buildoutput.RunsOnNode(buildoutput.FrameworkSvelteKit) {
		t.Error("a SvelteKit function is served by something other than node")
	}
	if !buildoutput.BuildsWithItsOwnScript(buildoutput.FrameworkSvelteKit) {
		t.Error("a SvelteKit app is built by something other than its own build script")
	}
	for _, framework := range []string{buildoutput.FrameworkNode, buildoutput.FrameworkGo, buildoutput.FrameworkPython, buildoutput.FrameworkRust} {
		if buildoutput.BuildsWithItsOwnScript(framework) {
			t.Errorf("a %s app is said to build with its own script", framework)
		}
	}
}
