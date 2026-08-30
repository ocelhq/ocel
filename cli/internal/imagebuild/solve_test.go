package imagebuild

import (
	"slices"
	"testing"
)

func TestTheSolveMountsTheAppBesideThePlanAndNothingElse(t *testing.T) {
	opt, err := solveOptions("testdata/plainserver", t.TempDir())
	if err != nil {
		t.Fatalf("solveOptions() = %v", err)
	}

	mounted := make([]string, 0, len(opt.LocalMounts))
	for name := range opt.LocalMounts {
		mounted = append(mounted, name)
	}
	slices.Sort(mounted)
	if want := []string{"context", "dockerfile"}; !slices.Equal(mounted, want) {
		t.Errorf("the solve mounts %v, want %v — the app itself, and the directory holding "+PlanFileName, mounted, want)
	}
}

func TestTheSolveCarriesNoEnvironmentNoSecretsAndNoBuildArgs(t *testing.T) {
	opt, err := solveOptions("testdata/plainserver", t.TempDir())
	if err != nil {
		t.Fatalf("solveOptions() = %v", err)
	}

	if len(opt.Session) != 0 {
		t.Errorf("the solve attaches %d session providers, want none — a secret or a credential attached here is baked into the layers", len(opt.Session))
	}
	if len(opt.FrontendAttrs) != 0 {
		t.Errorf("the solve carries frontend options %v, want none — railpack reads build args from these", opt.FrontendAttrs)
	}
	if opt.Frontend != "" {
		t.Errorf("the solve names frontend %q, want none: the frontend is linked into this binary, and naming one makes buildkitd pull an image", opt.Frontend)
	}
}

func TestTheSolveExportsIntoTheDaemonsOwnImageStore(t *testing.T) {
	opt, err := solveOptions("testdata/plainserver", t.TempDir())
	if err != nil {
		t.Fatalf("solveOptions() = %v", err)
	}

	if len(opt.Exports) != 1 || opt.Exports[0].Type != mobyExporter {
		t.Fatalf("the solve exports %+v, want one %q export — the image lands where the daemon keeps images", opt.Exports, mobyExporter)
	}
	if len(opt.Exports[0].Attrs) != 0 {
		t.Errorf("the export is named %v before the build has a digest, and a digest-derived tag cannot be known that early", opt.Exports[0].Attrs)
	}
}
