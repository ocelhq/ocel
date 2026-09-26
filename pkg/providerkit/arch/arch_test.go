package arch_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/arch"
)

func TestEveryArchitectureNamesTheStaticTargetRustCompilesFor(t *testing.T) {
	t.Parallel()

	for declared, want := range map[string]string{
		arch.X8664: "x86_64-unknown-linux-musl",
		arch.ARM64: "aarch64-unknown-linux-musl",
		"":         "x86_64-unknown-linux-musl",
	} {
		target, known := arch.RustTarget(declared)
		if !known || target != want {
			t.Errorf("RustTarget(%q) = %q, %v, want %q — a binary linked against no libc runs on every host the function lands on", declared, target, known, want)
		}
	}
	if target, known := arch.RustTarget("riscv"); known {
		t.Errorf("RustTarget(\"riscv\") = %q, want no target: an architecture nothing runs compiles nothing", target)
	}
}

func TestEveryArchitectureNamesTheWheelsPythonDependenciesAreVendoredFor(t *testing.T) {
	t.Parallel()

	for declared, want := range map[string]string{
		arch.X8664: "manylinux2014_x86_64",
		arch.ARM64: "manylinux2014_aarch64",
	} {
		tag, known := arch.PythonPlatformTag(declared)
		if !known || tag != want {
			t.Errorf("PythonPlatformTag(%q) = %q, %v, want %q — a wheel built for another machine cannot be imported", declared, tag, known, want)
		}
	}
	if tag, known := arch.PythonPlatformTag("riscv"); known {
		t.Errorf("PythonPlatformTag(\"riscv\") = %q, want no tag: an architecture nothing runs vendors nothing", tag)
	}
}
