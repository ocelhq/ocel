package providerkit_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestPythonIsARuntimeAnAppMayDeclare(t *testing.T) {
	t.Parallel()

	if !providerkit.KnownRuntime(providerkit.RuntimePython) {
		t.Fatalf("Runtimes() = %v, and none of them is %q", providerkit.Runtimes(), providerkit.RuntimePython)
	}
}

func TestRustIsARuntimeAnAppMayDeclare(t *testing.T) {
	t.Parallel()

	if !providerkit.KnownRuntime(providerkit.RuntimeRust) {
		t.Fatalf("Runtimes() = %v, and none of them is %q", providerkit.Runtimes(), providerkit.RuntimeRust)
	}
}

func TestEveryArchitectureNamesTheStaticTargetRustCompilesFor(t *testing.T) {
	t.Parallel()

	for arch, want := range map[string]string{
		providerkit.ArchX8664: "x86_64-unknown-linux-musl",
		providerkit.ArchARM64: "aarch64-unknown-linux-musl",
		"":                    "x86_64-unknown-linux-musl",
	} {
		target, known := providerkit.RustTarget(arch)
		if !known || target != want {
			t.Errorf("RustTarget(%q) = %q, %v, want %q — a binary linked against no libc runs on every host the function lands on", arch, target, known, want)
		}
	}
	if target, known := providerkit.RustTarget("riscv"); known {
		t.Errorf("RustTarget(\"riscv\") = %q, want no target: an architecture nothing runs compiles nothing", target)
	}
}

func TestEveryArchitectureNamesTheWheelsPythonDependenciesAreVendoredFor(t *testing.T) {
	t.Parallel()

	for arch, want := range map[string]string{
		providerkit.ArchX8664: "manylinux2014_x86_64",
		providerkit.ArchARM64: "manylinux2014_aarch64",
	} {
		tag, known := providerkit.PythonPlatformTag(arch)
		if !known || tag != want {
			t.Errorf("PythonPlatformTag(%q) = %q, %v, want %q — a wheel built for another machine cannot be imported", arch, tag, known, want)
		}
	}
	if tag, known := providerkit.PythonPlatformTag("riscv"); known {
		t.Errorf("PythonPlatformTag(\"riscv\") = %q, want no tag: an architecture nothing runs vendors nothing", tag)
	}
}

func TestTheArchitecturesAreTheOnesEveryRuntimeSharesAVocabularyFor(t *testing.T) {
	t.Parallel()

	for _, arch := range []string{providerkit.ArchX8664, providerkit.ArchARM64} {
		if _, known := providerkit.GoArch(arch); !known {
			t.Errorf("GoArch(%q) names nothing go builds for", arch)
		}
		if _, known := providerkit.PythonPlatformTag(arch); !known {
			t.Errorf("PythonPlatformTag(%q) names no wheel platform", arch)
		}
	}
	if !slices.Contains(providerkit.Runtimes(), providerkit.RuntimeGo) {
		t.Error("Runtimes() no longer names the go runtime")
	}
}
