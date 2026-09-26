package appbuild_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
)

func TestPythonIsARuntimeAnAppMayDeclare(t *testing.T) {
	t.Parallel()

	if !appbuild.KnownFramework(appbuild.FrameworkPython) {
		t.Fatalf("Frameworks() = %v, and none of them is %q", appbuild.Frameworks(), appbuild.FrameworkPython)
	}
}

func TestRustIsARuntimeAnAppMayDeclare(t *testing.T) {
	t.Parallel()

	if !appbuild.KnownFramework(appbuild.FrameworkRust) {
		t.Fatalf("Frameworks() = %v, and none of them is %q", appbuild.Frameworks(), appbuild.FrameworkRust)
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
	if !slices.Contains(appbuild.Frameworks(), appbuild.FrameworkGo) {
		t.Error("Frameworks() no longer names the go framework")
	}
}
