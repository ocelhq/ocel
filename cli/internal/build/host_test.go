package build

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestRefusingANextAppNamesTheProviderThatDeclaresNoNextRuntimeDirectory(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Path: "/app/ocel.json", Provider: &project.Provider{ID: "gcp"}, Apps: []project.App{nextApp("web", "apps/web")}}

	err := RefuseNextFunctionsWithoutRuntimeDir(cfg, Host{})
	if err == nil {
		t.Fatal("RefuseNextFunctionsWithoutRuntimeDir = nil error, want web refused")
	}
	for _, want := range []string{`"gcp"`, `"web"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, missing %s", err, want)
		}
	}
}

func TestRefusingANextAppSaysTheProjectNamesNoProviderWhenItNamesNone(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Path: "/app/ocel.json", Apps: []project.App{nextApp("web", "apps/web")}}

	err := RefuseNextFunctionsWithoutRuntimeDir(cfg, Host{})
	if err == nil {
		t.Fatal("RefuseNextFunctionsWithoutRuntimeDir = nil error, want web refused")
	}
	for _, want := range []string{"ocel.json names no provider", `"web"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, missing %q", err, want)
		}
	}
}

func TestReadHostSaysWhetherTheProviderShipsANextServerRuntime(t *testing.T) {
	t.Parallel()

	if !ReadHost(&contractv1.ProviderFacts{ShipsNextServerRuntime: true}).ShipsNextServerRuntime {
		t.Error("ReadHost() ShipsNextServerRuntime = false, want true for a provider that ships one")
	}
	if ReadHost(&contractv1.ProviderFacts{}).ShipsNextServerRuntime {
		t.Error("ReadHost() ShipsNextServerRuntime = true, want false for a provider that ships none")
	}
}
