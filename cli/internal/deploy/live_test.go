//go:build integration

package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/incustest"
)

func TestLiveADryRunOfAContainerAppSendsTheDigestTheDaemonBuilt(t *testing.T) {
	vm := incustest.Require(t)
	vm.Engine(t)
	vm.Forward(t)

	dependencies := newTestDependencies()

	fixture := setUpDeployProject(t)
	root := fixture.Root
	writeAppsConfig(t, root, `{ name: "api", path: "apps/api", compute: "container" }`)
	copyFixtureApp(t, "../../build/image/testdata/dockerfileapp", filepath.Join(root, "apps", "api"))

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true, dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy --dry err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	ref := manifestApp(t, sentDeploy(t, fixture).GetManifest(), "api").GetContainer().GetImage()
	if repository, digest, pinned := strings.Cut(ref, "@sha256:"); !pinned || repository != "ocel/"+clitest.FixtureSlug+"/api" || len(digest) != 64 {
		t.Fatalf("the dry run sent image %q, want it pinned at ocel/%s/api@sha256:<digest>", ref, clitest.FixtureSlug)
	}
	if _, err := vm.Attempt("docker image inspect " + ref); err != nil {
		t.Errorf("the dry run sent %s and the daemon has no image there, so it planned a coordinate no release could be pinned to: %v", ref, err)
	}
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
