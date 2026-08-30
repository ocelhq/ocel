package imagebuild

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/moby/buildkit/client"
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
	if opt.LocalMounts[frontendMount] == nil {
		t.Fatalf("the solve mounts %v, and the plan is not among them under %q, the mount railpack reads its plan from", opt.LocalMounts, frontendMount)
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

func dockerfileSolve(t *testing.T) client.SolveOpt {
	t.Helper()
	choice, err := Choose(App{Name: "web", Dir: "testdata/dockerfileapp"})
	if err != nil {
		t.Fatalf("Choose() = %v", err)
	}
	opt, done, err := choice.solve()
	if err != nil {
		t.Fatalf("solve() = %v", err)
	}
	t.Cleanup(done)
	return opt
}

func TestADockerfileBuildIsHandedToTheFrontendTheDaemonAlreadyHas(t *testing.T) {
	opt := dockerfileSolve(t)

	if opt.Frontend != dockerfileFrontend {
		t.Errorf("the solve names frontend %q, want %q — the one buildkitd carries, so no frontend image is pulled", opt.Frontend, dockerfileFrontend)
	}
	if want := map[string]string{filenameAttr: DockerfileName}; !maps.Equal(opt.FrontendAttrs, want) {
		t.Errorf("the solve carries frontend options %v, want %v — the file to read, and no build args", opt.FrontendAttrs, want)
	}
	if len(opt.Session) != 0 || len(opt.FrontendInputs) != 0 {
		t.Errorf("the solve attaches %d session providers and %d frontend inputs, want none of either", len(opt.Session), len(opt.FrontendInputs))
	}
}

func TestADockerfileBuildsTheAppDirectoryWhereverTheDockerfileLives(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "api")
	shared := filepath.Join(root, "shared")
	for _, dir := range []string{appDir, shared} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(shared, "Node.Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	opt, err := dockerfileOptions(appDir, filepath.Join(shared, "Node.Dockerfile"))
	if err != nil {
		t.Fatalf("dockerfileOptions() = %v", err)
	}

	if opt.FrontendAttrs[filenameAttr] != "Node.Dockerfile" {
		t.Errorf("the frontend reads %q, want the file the app was pointed at", opt.FrontendAttrs[filenameAttr])
	}
	mounted := make([]string, 0, len(opt.LocalMounts))
	for name := range opt.LocalMounts {
		mounted = append(mounted, name)
	}
	slices.Sort(mounted)
	if want := []string{contextMount, frontendMount}; !slices.Equal(mounted, want) {
		t.Fatalf("the solve mounts %v, want %v", mounted, want)
	}
	held, err := opt.LocalMounts[contextMount].Open("Node.Dockerfile")
	if err == nil {
		_ = held.Close()
		t.Error("the build context holds the shared Dockerfile, so the context is the directory that file is in rather than the app's own")
	}
}

func TestNeitherBuilderExportsTheImageDifferently(t *testing.T) {
	railpack, err := solveOptions("testdata/plainserver", t.TempDir())
	if err != nil {
		t.Fatalf("solveOptions() = %v", err)
	}
	dockerfile := dockerfileSolve(t)

	if !reflect.DeepEqual(railpack.Exports, dockerfile.Exports) {
		t.Errorf("railpack exports %+v and a Dockerfile exports %+v: the digest the coordinate is derived from would come from two different places", railpack.Exports, dockerfile.Exports)
	}
}
