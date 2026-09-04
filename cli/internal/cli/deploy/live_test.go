package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/livemachine"
)

func TestLiveADryRunOfAContainerAppCarriesTheDigestTheDaemonBuilt(t *testing.T) {
	vm := livemachine.Require(t)
	vm.Engine(t)
	vm.Forward(t)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)

	root, sockPath := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container,serverless")
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { package: "@ocel/provider-aws", options: {} },
  apps: [{ name: "api", path: "apps/api", compute: "container" }],
};
`)
	copyFixtureApp(t, "../../imagebuild/testdata/plainserver", filepath.Join(root, "apps", "api"))

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true, dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy --dry err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	ref := pinnedRefIn(t, stdout.String(), "ocel/"+clitest.FixtureSlug+"/api")
	if _, err := vm.Attempt("docker image inspect " + ref); err != nil {
		t.Errorf("the plan names %s and the daemon holds no image there, so the dry run rendered a coordinate no release could be pinned to: %v", ref, err)
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
}

func pinnedRefIn(t *testing.T, out, repository string) string {
	t.Helper()
	for _, field := range strings.Fields(out) {
		named, digest, pinned := strings.Cut(field, "@sha256:")
		if !pinned || named != repository || len(digest) != 64 {
			continue
		}
		return field
	}
	t.Fatalf("the dry run rendered no image pinned at %s@sha256:<digest>:\n%s", repository, out)
	return ""
}

func copyFixtureApp(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(from, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		clitest.WriteFile(t, filepath.Join(to, entry.Name()), string(body))
	}
}
