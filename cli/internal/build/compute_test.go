package build

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
)

func fromFunction(a project.App) project.App {
	container := *a.Container
	container.FromFunction = true
	a.Container = &container
	return a
}

func buildBothContainerPaths(t *testing.T, archs map[string]string) (imageArch, functionArch string) {
	t.Helper()
	cfg := projectOfApps(t, fromFunction(nextContainerApp("web", "web")), project.App{Name: "api", Path: "api", Compute: provider.ComputeContainer, Container: &project.Container{}})
	writeBuildScript(t, cfg.Dir)
	builder := tools{
		buildScript: func() (string, error) { return testBuildScript(cfg.Dir), nil },
		node: func(_ context.Context, _ string, request []byte, _ Log) error {
			var got nodeBuildRequest
			if err := json.Unmarshal(request, &got); err != nil {
				return err
			}
			for _, app := range got.Apps {
				functionArch = app.Arch
				writeFuncConfig(t, filepath.Dir(filepath.Dir(app.OutputDir)), app.Name, "index.func",
					buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next", Arch: app.Arch}, EntryFile: "index.handler", ID: "/", App: app.Name})
			}
			return nil
		},
		image: func(_ context.Context, app image.App, arch string, _ image.LiveValues, _ io.Writer) (image.Image, error) {
			imageArch = arch
			return image.Image{Ref: "ocel/shop/" + app.Name + "@sha256:" + strings.Repeat("a", 64)}, nil
		},
	}
	if _, err := builder.apps(context.Background(), cfg, nil, archs, nil, Host{}, Log{}); err != nil {
		t.Fatalf("apps() = %v", err)
	}
	return imageArch, functionArch
}

func TestBothContainerPathsAreBuiltForTheArchitectureTheTargetRuns(t *testing.T) {
	for _, runs := range []string{"amd64", "arm64"} {
		imageArch, functionArch := buildBothContainerPaths(t, map[string]string{"web": runs, "api": runs})
		if built, _ := arch.GoArch(functionArch); imageArch != runs || built != runs {
			t.Errorf("for a target that runs %s, the image is built for %q and the function for %q, want both for %s", runs, imageArch, functionArch, runs)
		}
	}
}

func TestBothContainerPathsAreBuiltForTheBuildingMachineWhenNothingNamesAnArchitecture(t *testing.T) {
	imageArch, functionArch := buildBothContainerPaths(t, map[string]string{"web": "", "api": ""})
	if imageArch != "" {
		t.Errorf("the image is built for %q, want the docker daemon's own platform", imageArch)
	}
	if built, _ := arch.GoArch(functionArch); built != runtime.GOARCH {
		t.Errorf("the function is built for %q, want %s, the machine building it, as the image is", functionArch, runtime.GOARCH)
	}
}

func TestAPrebuiltFunctionForAnotherArchitectureThanTheTargetRunsIsRefusedAsAPrebuiltImageIs(t *testing.T) {
	cfg := projectOfApps(t, fromFunction(nextContainerApp("web", "web")))
	writeFuncConfig(t, outputRoot(t, cfg.Dir), "web", "index.func",
		buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next", Arch: arch.ARM64}, EntryFile: "index.handler", ID: "/", App: "web"})

	_, err := tools{}.readPrebuilt(context.Background(), cfg, map[string]string{"web": "amd64"})
	want := "app \"web\" was prebuilt for arm64, and its target runs x86_64: run `ocel build` again with `arch: \"x86_64\"` on \"web\""
	if err == nil || err.Error() != want {
		t.Fatalf("readPrebuilt() = %v, want %q", err, want)
	}

	if _, err := (tools{}).readPrebuilt(context.Background(), cfg, map[string]string{"web": "arm64"}); err != nil {
		t.Errorf("readPrebuilt() = %v, want the arm64 function taken for a target that runs arm64", err)
	}
}

func TestANextContainerBuiltFromItsFunctionIsHandedToTheNodeBuildWithItsValues(t *testing.T) {
	root := t.TempDir()
	writeBuildScript(t, root)
	cfg := &project.Project{Dir: root, Apps: []project.App{fromFunction(nextContainerApp("web", "apps/web"))}}

	var got nodeBuildRequest
	builder := nodeOnly{node: requestOf(&got)}
	if err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": {Env: map[string]string{"NEXT_PUBLIC_API": "https://api"}}}, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(got.Apps) != 1 || got.Apps[0].Name != "web" || got.Apps[0].Framework != buildoutput.FrameworkNext {
		t.Fatalf("request = %+v, want web built by ocel's Next adapter", got.Apps)
	}
	if web := got.Apps[0]; web.Env["NEXT_PUBLIC_API"] != "https://api" {
		t.Errorf("the build reads NEXT_PUBLIC_API=%q, want the value the app resolves, which next build inlines", web.Env["NEXT_PUBLIC_API"])
	}
}

func TestANextContainerBuiltFromItsFunctionIsBuiltForTheEdgeOnlyWhereTheEdgesCodeDispatchesIt(t *testing.T) {
	limits := CacheTagLimits{TagBytes: 1024, Tags: 1000, HeaderBytes: 16 << 10}
	for _, testCase := range []struct {
		name       string
		dispatches bool
	}{
		{"an edge whose code dispatches containers", true},
		{"an edge that forwards containers past its code", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeBuildScript(t, root)
			cfg := &project.Project{Dir: root, Apps: []project.App{fromFunction(nextContainerApp("web", "apps/web")), nextApp("docs", "apps/docs")}}
			host := Host{MaxFunctionBytes: 200 << 20, EdgeRunsCode: true, EdgeDispatchesContainers: testCase.dispatches, EdgeCacheTagLimits: limits}

			var got nodeBuildRequest
			builder := nodeOnly{node: requestOf(&got), host: host}
			if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
				t.Fatalf("Build: %v", err)
			}

			web, docs := got.Apps[0], got.Apps[1]
			if web.MaxFunctionBytes != 0 {
				t.Errorf("web is built against budget %d, want none: a container runs its one function whole", web.MaxFunctionBytes)
			}
			builtForTheEdge := web.EdgeRunsCode && web.EdgeCacheTagLimits != nil && web.EdgeCacheTagLimits.TagBytes == limits.TagBytes
			builtForNoEdge := !web.EdgeRunsCode && web.EdgeCacheTagLimits == nil
			if testCase.dispatches && !builtForTheEdge || !testCase.dispatches && !builtForNoEdge {
				t.Errorf("web is built with edge runs code %v and tag limits %+v, want the edge's facts only where its code dispatches web (%v)", web.EdgeRunsCode, web.EdgeCacheTagLimits, testCase.dispatches)
			}
			if !docs.EdgeRunsCode || docs.MaxFunctionBytes != 200<<20 {
				t.Errorf("docs is built with edge runs code %v and budget %d, want the provider's facts a serverless Next app is built with", docs.EdgeRunsCode, docs.MaxFunctionBytes)
			}
		})
	}
}

func TestTheNodeBuildIsHandedTheArchitectureOnlyANextContainersFunctionIsBuiltFor(t *testing.T) {
	root := t.TempDir()
	writeBuildScript(t, root)
	cfg := &project.Project{Dir: root, Apps: []project.App{fromFunction(nextContainerApp("web", "apps/web")), nextApp("docs", "apps/docs")}}

	var got nodeBuildRequest
	builder := nodeOnly{node: requestOf(&got), archs: map[string]string{"web": "arm64"}}
	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, app := range got.Apps {
		if want := map[string]string{"web": arch.ARM64, "docs": ""}[app.Name]; app.Arch != want {
			t.Errorf("%s is built for %q, want %q: a serverless function's architecture is its provider's to choose", app.Name, app.Arch, want)
		}
	}
}

func projectOfApps(t *testing.T, apps ...project.App) *project.Project {
	t.Helper()
	root := t.TempDir()
	for _, a := range apps {
		writeFile(t, filepath.Join(root, a.Path, "package.json"), "{}\n")
	}
	return &project.Project{Dir: root, Slug: "shop", Apps: apps}
}

func namesOf(apps []project.App) []string {
	names := make([]string, 0, len(apps))
	for _, a := range apps {
		names = append(names, a.Name)
	}
	return names
}

var functionsHost = Host{RunsServerless: true}

func TestANextContainerNoDockerfileBuildsIsBuiltByTheAdapterOnlyWhereNextStartCannotServeIt(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		host    Host
		adapted bool
	}{
		{"a provider that runs Next as functions keeps the cache its instances share", Host{RunsServerless: true}, true},
		{"an edge whose code dispatches containers serves the app from its build's records", Host{EdgeDispatchesContainers: true}, true},
		{"on a box nothing dispatches next start serves the app as it is", Host{}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolved, err := ResolveNextContainerBuilds(projectOfApps(t, nextContainerApp("web", "web")), testCase.host)
			if err != nil {
				t.Fatalf("ResolveNextContainerBuilds() = %v", err)
			}
			if got := resolved.Apps[0].IsBuiltFromFunction(); got != testCase.adapted {
				t.Errorf("web built by ocel's Next adapter = %v, want %v", got, testCase.adapted)
			}
		})
	}
}

func TestANextContainerNextStartServesKeepsTheImageSettingsItsImageBuildReads(t *testing.T) {
	app := nextContainerApp("web", "web")
	app.Container = &project.Container{Build: &project.Build{Command: "pnpm build", Context: "."}}

	resolved, err := ResolveNextContainerBuilds(projectOfApps(t, app), Host{})
	if err != nil {
		t.Fatalf("ResolveNextContainerBuilds() = %v, want image.command and image.context kept for the image build that reads them", err)
	}
	if got := namesOf(ImageApps(resolved.Apps)); !slices.Equal(got, []string{"web"}) {
		t.Errorf("ImageApps() = %v, want web built as an image", got)
	}
}

func TestAProjectReadsItsHostsFactsOnlyForANextAppTheAdapterMayBuild(t *testing.T) {
	dockerfiled := nextContainerApp("docker", "docker")
	cfg := projectOfApps(t, dockerfiled, project.App{Name: "api", Path: "api", Compute: provider.ComputeContainer, Framework: buildoutput.FrameworkNode, Container: &project.Container{}})
	writeFile(t, filepath.Join(cfg.Dir, "docker", "Dockerfile"), "FROM scratch\n")
	if hostless, err := CanBuildWithoutHost(cfg); err != nil || !hostless {
		t.Errorf("CanBuildWithoutHost(Dockerfile Next container, node container) = %v, %v; want true: neither is built by the adapter", hostless, err)
	}

	for _, app := range []project.App{nextApp("web", "web"), nextContainerApp("web", "web")} {
		if hostless, err := CanBuildWithoutHost(projectOfApps(t, app)); err != nil || hostless {
			t.Errorf("CanBuildWithoutHost(%s on %s) = %v, %v; want false: whether and how the adapter builds it is its host's to say", app.Name, app.Compute, hostless, err)
		}
	}
}

func TestANextContainerNoDockerfileBuildsIsBuiltFromItsFunction(t *testing.T) {
	cfg := projectOfApps(t, nextContainerApp("web", "web"))

	resolved, err := ResolveNextContainerBuilds(cfg, functionsHost)
	if err != nil {
		t.Fatalf("ResolveNextContainerBuilds() = %v", err)
	}
	if got := namesOf(FunctionApps(resolved.Apps)); !slices.Equal(got, []string{"web"}) {
		t.Errorf("FunctionApps() = %v, want web built by ocel's Next adapter as a serverless Next app is", got)
	}
	if got := namesOf(ImageApps(resolved.Apps)); len(got) != 0 {
		t.Errorf("ImageApps() = %v, want none: no image is built for web on this machine", got)
	}
	if !resolved.Apps[0].RunsOn(provider.ComputeContainer) {
		t.Errorf("web runs on %q, want the container compute it asked for", resolved.Apps[0].Compute)
	}
	if !CanReadVariablesAtBuild(resolved.Apps[0]) {
		t.Error("web reads no value while it builds, and next build inlines its public values as a serverless Next build does")
	}
	if cfg.Apps[0].IsBuiltFromFunction() {
		t.Error("resolving rewrote the project it was handed, want a resolved copy")
	}
}

func TestANextContainerADockerfileBuildsKeepsItsImage(t *testing.T) {
	named := nextContainerApp("named", "named")
	named.Container = &project.Container{Build: &project.Build{Dockerfile: "prod.Dockerfile", Command: "pnpm build"}}
	cfg := projectOfApps(t, nextContainerApp("beside", "beside"), named)
	writeFile(t, filepath.Join(cfg.Dir, "beside", "Dockerfile"), "FROM scratch\n")
	writeFile(t, filepath.Join(cfg.Dir, "named", "prod.Dockerfile"), "FROM scratch\n")

	resolved, err := ResolveNextContainerBuilds(cfg, functionsHost)
	if err != nil {
		t.Fatalf("ResolveNextContainerBuilds() = %v", err)
	}
	if got := namesOf(ImageApps(resolved.Apps)); !slices.Equal(got, []string{"beside", "named"}) {
		t.Errorf("ImageApps() = %v, want both apps built from their Dockerfiles", got)
	}
}

func TestAContainerThatServesNoNextKeepsItsImage(t *testing.T) {
	api := project.App{Name: "api", Path: "api", Compute: provider.ComputeContainer, Framework: buildoutput.FrameworkNode, Container: &project.Container{}}

	resolved, err := ResolveNextContainerBuilds(projectOfApps(t, api, nextApp("web", "web")), functionsHost)
	if err != nil {
		t.Fatalf("ResolveNextContainerBuilds() = %v", err)
	}
	if got := namesOf(ImageApps(resolved.Apps)); !slices.Equal(got, []string{"api"}) {
		t.Errorf("ImageApps() = %v, want api built as an image", got)
	}
	if got := namesOf(FunctionApps(resolved.Apps)); !slices.Equal(got, []string{"web"}) {
		t.Errorf("FunctionApps() = %v, want only the serverless web", got)
	}
}

func TestANextContainerBuiltFromItsFunctionRefusesTheImageSettingsOnlyAnImageBuildReads(t *testing.T) {
	for setting, build := range map[string]*project.Build{
		"image.command": {Command: "pnpm build"},
		"image.context": {Context: "."},
	} {
		t.Run(setting, func(t *testing.T) {
			app := nextContainerApp("web", "web")
			app.Container = &project.Container{Build: build}

			_, err := ResolveNextContainerBuilds(projectOfApps(t, app), functionsHost)
			want := `app "web" is built by ocel's Next adapter, which reads no ` + setting + `: drop ` + setting
			if err == nil || err.Error() != want {
				t.Fatalf("ResolveNextContainerBuilds() = %v, want %q", err, want)
			}
		})
	}
}

func TestANextContainerSettingBothImageSettingsIsRefusedForBoth(t *testing.T) {
	app := nextContainerApp("web", "web")
	app.Container = &project.Container{Build: &project.Build{Command: "pnpm build", Context: "."}}

	_, err := ResolveNextContainerBuilds(projectOfApps(t, app), functionsHost)
	if err == nil || !strings.HasSuffix(err.Error(), "reads no image.command or image.context: drop them") {
		t.Fatalf("ResolveNextContainerBuilds() = %v, want both settings refused with one fix", err)
	}
}
