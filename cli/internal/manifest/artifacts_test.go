package manifest

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const fakeDigest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerOf(t *testing.T, m *contractv1.Manifest, app string) *contractv1.ContainerArtifact {
	t.Helper()
	var names []string
	for _, a := range m.GetApps() {
		if a.GetName() == app && a.GetContainer() != nil {
			return a.GetContainer()
		}
		names = append(names, a.GetName())
	}
	t.Fatalf("manifest has no container for app %q, only the apps %v", app, names)
	return nil
}

func containerApps(m *contractv1.Manifest) []string {
	var apps []string
	for _, a := range m.GetApps() {
		if a.GetContainer() != nil {
			apps = append(apps, a.GetName())
		}
	}
	return apps
}

func TestAContainerAppCarriesItsImageAsItsArtifact(t *testing.T) {
	t.Parallel()

	manifest, err := assembleOn("serverless", "proj-1", project.Domains{}, []app{
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest},
		{Name: "web", Compute: "serverless"},
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if got := containerApps(manifest); len(got) != 1 {
		t.Fatalf("manifest carries containers for %v, want only the one container app", got)
	}
	container := containerOf(t, manifest, "api")
	if got, want := container.GetImage(), "ocel/api@"+fakeDigest; got != want {
		t.Errorf("container image = %q, want %q", got, want)
	}
}

func TestAContainerNamesTheHealthPathTheAppAsksFor(t *testing.T) {
	t.Parallel()

	manifest, err := assembleOn("container", "proj-1", project.Domains{}, []app{
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest, HealthCheckPath: "/healthz"},
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if got, want := containerOf(t, manifest, "api").GetHealthCheckPath(), "/healthz"; got != want {
		t.Errorf("health_check_path = %q, want %q", got, want)
	}
}

func TestAContainerNamesTheArchitectureItsAppDeclares(t *testing.T) {
	t.Parallel()

	manifest, err := assembleOn("container", "proj-1", project.Domains{}, []app{
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest, Arch: "arm64"},
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if got := containerOf(t, manifest, "api").GetArch(); got != "arm64" {
		t.Errorf("arch = %q, want arm64: a container app names no framework, so the container is the only place its architecture rides", got)
	}
}

func TestAContainerThatAsksForNoHealthPathIsWrittenWithTheDefaultOne(t *testing.T) {
	t.Parallel()

	manifest, err := assembleOn("container", "proj-1", project.Domains{}, []app{
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest},
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if got, want := containerOf(t, manifest, "api").GetHealthCheckPath(), defaultHealthCheckPath; got != want {
		t.Errorf("health_check_path = %q, want the default resolved here so no provider resolves one of its own", got)
	}
}

func TestAnAppOnlyTheBuildNamesCannotLandOnContainerCompute(t *testing.T) {
	t.Parallel()

	_, err := assembleOn("container", "proj-1", project.Domains{}, nil, nil, nil, []build.Function{
		{App: "api", Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.handler", ArtifactPath: "apps/api/functions/index"},
	}, nil)
	if err == nil {
		t.Fatal("assemble() landed an app the config never names on container compute, and nothing would have told a provider what image to run")
	}
	for _, want := range []string{`"api"`, "apps"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("assemble() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAContainerAppWithNoImageRefusesTheManifest(t *testing.T) {
	t.Parallel()

	_, err := assembleOn("container", "proj-1", project.Domains{}, []app{{Name: "api", Compute: "container"}}, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("assemble() included a container app with no image, so a provider would be handed an app it has nothing to run")
	}
	if !strings.Contains(err.Error(), `"api"`) {
		t.Errorf("assemble() error = %q, want it to name the app", err)
	}
}

func TestAnImageNamesADigestAndNeverATag(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{"ocel/api:latest", "ocel/api", "ocel/api@sha256:short"} {
		_, err := assembleOn("container", "proj-1", project.Domains{}, []app{{Name: "api", Compute: "container", Image: ref}}, nil, nil, nil, nil)
		if err == nil {
			t.Errorf("assemble() kept %q as an image identity, want only a digest-pinned ref, since a tag is repointable and a release is not", ref)
		}
	}
}

func TestAServerlessAppIsWrittenAsNoContainerAtAll(t *testing.T) {
	t.Parallel()

	manifest, err := assembleOn("serverless", "proj-1", project.Domains{}, []app{{Name: "web"}}, nil, nil, []build.Function{
		{App: "web", Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.handler", ArtifactPath: "apps/web/functions/index"},
	}, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := containerApps(manifest); len(got) != 0 {
		t.Errorf("manifest carries containers for %v, want no container for an app that runs serverless", got)
	}
}

func TestAContainerAppIsServedByItsImageAloneWhateverFunctionsTheBuildLeftForIt(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Slug: "proj-1", Dir: t.TempDir(), Apps: []project.App{
		{Name: "api", Path: "api", Compute: "container", Container: &project.Container{}},
	}}
	manifest, err := Assemble(Input{
		Project: cfg,
		Built: build.Output{
			Functions: []build.Function{{App: "api", Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "index.handler", ArtifactPath: "apps/api/functions/index"}},
			Images:    map[string]string{"api": "ocel/api@" + fakeDigest},
		},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	apps := manifest.GetApps()
	if len(apps) != 1 || apps[0].GetContainer().GetImage() == "" || apps[0].GetServerless() != nil {
		t.Errorf("apps = %v, container apps = %v, want api served by its image and by no function, so routing has one answer per request", appNames(apps), containerApps(manifest))
	}
}

func TestContainersAreOrderedByTheAppTheyServe(t *testing.T) {
	t.Parallel()

	manifest, err := assembleOn("container", "proj-1", project.Domains{}, []app{
		{Name: "web", Compute: "container", Image: "ocel/web@" + fakeDigest},
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest},
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	order := containerApps(manifest)
	if len(order) != 2 || order[0] != "api" || order[1] != "web" {
		t.Errorf("containers ordered %v, want them ordered by app so the same project builds the same manifest twice", order)
	}
}
