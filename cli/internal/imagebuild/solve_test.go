package imagebuild

import (
	"os"
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

func TestTheMountedPlanIsTheFileTheFrontendReads(t *testing.T) {
	dir, err := stagePlan([]byte(`{"steps":[]}`))
	if err != nil {
		t.Fatalf("stagePlan() = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	staged := make([]string, 0, len(entries))
	for _, entry := range entries {
		staged = append(staged, entry.Name())
	}
	if want := []string{PlanFileName}; !slices.Equal(staged, want) {
		t.Fatalf("the mounted directory holds %v, want %v — railpack's frontend reads that name and nothing else from it", staged, want)
	}

	opt, err := solveOptions("testdata/plainserver", dir)
	if err != nil {
		t.Fatalf("solveOptions() = %v", err)
	}
	if opt.LocalMounts[planMount] == nil {
		t.Fatalf("the solve mounts %v, and the plan is not among them under %q, the mount railpack reads its plan from", opt.LocalMounts, planMount)
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
	if len(opt.FrontendInputs) != 0 {
		t.Errorf("the solve carries frontend inputs %v, want none — a state handed to the frontend here is a second way into the build", opt.FrontendInputs)
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
