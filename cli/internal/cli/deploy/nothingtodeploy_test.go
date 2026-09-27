package deploy

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func nothingToDeployHeadline(t *testing.T, config string) string {
	t.Helper()
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)
	useJSONLogFormat(t, &deps)
	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), config)
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), "export {};\n")
	writeAppSource(t, root, "web", "api")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stdout)
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}
	evs := envelopes(t, stdout.String())
	return evs[len(evs)-1].GetResult().GetHeadline()
}

func TestAProjectWithoutAppsOrResourcesHasNothingToDeploy(t *testing.T) {
	headline := nothingToDeployHeadline(t, `
export default {
  slug: "test-app",
  provider: { aws: { region: "eu-west-2" } },
};
`)
	if want := "Nothing to deploy: test-app declares no apps or resources"; headline != want {
		t.Errorf("headline = %q, want %q", headline, want)
	}
}

func TestAppsThatBuildNoFunctionOrImageAreNamedWhenNothingIsLeftToDeploy(t *testing.T) {
	headline := nothingToDeployHeadline(t, `
export default {
  slug: "test-app",
  provider: { aws: { region: "eu-west-2" } },
  apps: [
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", framework: "node" },
  ],
};
`)
	if want := "Nothing to deploy: 2 apps (web and api) built no function or image, and test-app declares no resources"; headline != want {
		t.Errorf("headline = %q, want %q", headline, want)
	}
}
