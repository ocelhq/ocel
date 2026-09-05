package manifestbuilder

import (
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const fakeDigest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerOf(t *testing.T, m *contractv1.Manifest, app string) *contractv1.ManifestContainer {
	t.Helper()
	for _, c := range m.GetContainers() {
		if c.GetApp() == app {
			return c
		}
	}
	t.Fatalf("manifest carries no container for app %q: %+v", app, m.GetContainers())
	return nil
}

func TestAContainerAppIsCarriedAsASiblingJoinedToItByName(t *testing.T) {
	t.Parallel()

	manifest, err := Build("proj-1", nil, []App{
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest},
		{Name: "web", Compute: "serverless"},
	}, "serverless", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(manifest.GetContainers()) != 1 {
		t.Fatalf("manifest carries %d containers, want only the one container app: %+v", len(manifest.GetContainers()), manifest.GetContainers())
	}
	container := containerOf(t, manifest, "api")
	if got, want := container.GetImage(), "ocel/api@"+fakeDigest; got != want {
		t.Errorf("container image = %q, want %q", got, want)
	}
}

func TestAContainerCarriesTheHealthPathTheAppAsksFor(t *testing.T) {
	t.Parallel()

	manifest, err := Build("proj-1", nil, []App{
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest, HealthCheckPath: "/healthz"},
	}, "container", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got, want := containerOf(t, manifest, "api").GetHealthCheckPath(), "/healthz"; got != want {
		t.Errorf("health_check_path = %q, want %q", got, want)
	}
}

func TestAContainerThatAsksForNoHealthPathIsCarriedWithTheDefaultOne(t *testing.T) {
	t.Parallel()

	manifest, err := Build("proj-1", nil, []App{
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest},
	}, "container", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got, want := containerOf(t, manifest, "api").GetHealthCheckPath(), DefaultHealthCheckPath; got != want {
		t.Errorf("health_check_path = %q, want the default resolved here so no provider resolves one of its own", got)
	}
}

func TestAnAppOnlyTheBuildNamesCannotLandOnContainerCompute(t *testing.T) {
	t.Parallel()

	_, err := Build("proj-1", nil, nil, "container", nil, nil, []Function{
		{App: "api", Route: "index", Runtime: Runtime{Name: "node"}, Handler: "index.handler", ArtifactPath: "apps/api/functions/index"},
	}, nil)
	if err == nil {
		t.Fatal("Build() landed an app the config never names on container compute, and nothing would have told a provider what image to run")
	}
	for _, want := range []string{`"api"`, "apps"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Build() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAContainerAppWithNoImageRefusesTheManifest(t *testing.T) {
	t.Parallel()

	_, err := Build("proj-1", nil, []App{{Name: "api", Compute: "container"}}, "container", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("Build() carried a container app with no image, so a provider would be handed an app it has nothing to run")
	}
	if !strings.Contains(err.Error(), `"api"`) {
		t.Errorf("Build() error = %q, want it to name the app", err)
	}
}

func TestAnImageCarriesADigestAndNeverATag(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{"ocel/api:latest", "ocel/api", "ocel/api@sha256:short"} {
		_, err := Build("proj-1", nil, []App{{Name: "api", Compute: "container", Image: ref}}, "container", nil, nil, nil, nil)
		if err == nil {
			t.Errorf("Build() carried %q as an image identity, want only a digest-pinned ref, since a tag is repointable and a release is not", ref)
		}
	}
}

func TestAServerlessAppIsCarriedAsNoContainerAtAll(t *testing.T) {
	t.Parallel()

	manifest, err := Build("proj-1", nil, []App{{Name: "web"}}, "serverless", nil, nil, []Function{
		{App: "web", Route: "index", Runtime: Runtime{Name: "node"}, Handler: "index.handler", ArtifactPath: "apps/web/functions/index"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(manifest.GetContainers()) != 0 {
		t.Errorf("manifest carries %+v, want no container for an app that runs serverless", manifest.GetContainers())
	}
}

func TestACallerThatPacksAContainerAppIsRefusedByTheBuilder(t *testing.T) {
	t.Parallel()

	_, err := Build("proj-1", nil, []App{
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest},
	}, "container", nil, nil, []Function{
		{App: "api", Route: "index", Runtime: Runtime{Name: "node"}, Handler: "index.handler", ArtifactPath: "apps/api/functions/index"},
	}, nil)
	if err == nil {
		t.Fatal("Build() carried both a container and a function for one app, so routing would have two answers for the same request")
	}
	if !strings.Contains(err.Error(), `"api"`) {
		t.Errorf("Build() error = %q, want it to name the app", err)
	}
}

func TestContainersAreOrderedByTheAppTheyServe(t *testing.T) {
	t.Parallel()

	manifest, err := Build("proj-1", nil, []App{
		{Name: "web", Compute: "container", Image: "ocel/web@" + fakeDigest},
		{Name: "api", Compute: "container", Image: "ocel/api@" + fakeDigest},
	}, "container", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var order []string
	for _, c := range manifest.GetContainers() {
		order = append(order, c.GetApp())
	}
	if len(order) != 2 || order[0] != "api" || order[1] != "web" {
		t.Errorf("containers ordered %v, want them ordered by app so the same project builds the same manifest twice", order)
	}
}
