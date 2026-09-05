package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	edge "github.com/ocelhq/ocel/platform/edge/contract"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestDeployResult(t *testing.T) {
	t.Run("a successful deploy records the promotion, the tag and every app", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, []manifestbuilder.Function{{
			Route: "api", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js",
			ArtifactPath: "output/api", App: "api",
		}})
		root, _ := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)
		writeServeDescriptor(t, root, "api", "bld_api_1")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true, tag: "v9"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readDeployResult(t, root)
		if got.SchemaVersion != deployresult.SchemaVersion {
			t.Errorf("schemaVersion = %d, want %d", got.SchemaVersion, deployresult.SchemaVersion)
		}
		if got.Slug != "test-app" {
			t.Errorf("slug = %q, want the resolved config's", got.Slug)
		}
		if got.Environment.Class != "production" {
			t.Errorf("environment.class = %q, want %q", got.Environment.Class, "production")
		}
		if got.PromotionID != clitest.FakePromotionID {
			t.Errorf("promotionId = %q, want the provider's %q", got.PromotionID, clitest.FakePromotionID)
		}
		if got.Tag != "v9" {
			t.Errorf("tag = %q, want %q", got.Tag, "v9")
		}
		if len(got.Apps) == 0 || len(got.Apps[0].URLs) != 1 || got.Apps[0].URLs[0] != clitest.FakeAppURL {
			t.Errorf("apps = %+v, want the first app to carry [%s]", got.Apps, clitest.FakeAppURL)
		}
		if len(got.Apps) != 1 || got.Apps[0].Name != "api" || got.Apps[0].BuildID != "bld_api_1" {
			t.Errorf("apps = %+v, want one api app with build id bld_api_1", got.Apps)
		}
		if got.DeployedAt.IsZero() {
			t.Error("deployedAt is zero, want the completion time")
		}
	})

	t.Run("a failed deploy leaves no stale result behind", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		root, _ := clitest.SetUpDeployFixture(t)
		if err := deployresult.Write(root, deployresult.Result{PromotionID: "prm_previous_run"}); err != nil {
			t.Fatalf("seed stale result: %v", err)
		}
		t.Setenv(clitest.FakeProviderModeEnvVar, "fail")

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the simulated failure; stdout=%s", stdout.String())
		}

		if _, statErr := os.Stat(deployresult.Path(root)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want no result file after a failed deploy", deployresult.Path(root), statErr)
		}
	})

	t.Run("a successful preview up records the named preview", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, []manifestbuilder.Function{{
			Route: "api", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js",
			ArtifactPath: "output/api", App: "api",
		}})
		root, _ := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)
		writeServeDescriptor(t, root, "api", "bld_api_1")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")

		var stdout, stderr bytes.Buffer
		if err := runPreviewUp(context.Background(), deps, root, previewUpOptions{name: "e2e-42"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readDeployResult(t, root)
		if got.Environment.Class != "preview" || got.Environment.Identity != "e2e-42" {
			t.Errorf("environment = %+v, want the named preview", got.Environment)
		}
		if got.PromotionID != clitest.FakePromotionID {
			t.Errorf("promotionId = %q, want the provider's %q", got.PromotionID, clitest.FakePromotionID)
		}
		if len(got.Apps) == 0 || len(got.Apps[0].URLs) != 1 || got.Apps[0].URLs[0] != clitest.FakeAppURL {
			t.Errorf("apps = %+v, want the first app to carry [%s]", got.Apps, clitest.FakeAppURL)
		}
	})
}

func readDeployResult(t *testing.T, root string) deployresult.Result {
	t.Helper()
	raw, err := os.ReadFile(deployresult.Path(root))
	if err != nil {
		t.Fatalf("read deploy result: %v", err)
	}
	var got deployresult.Result
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("deploy result is not valid JSON: %v", err)
	}
	return got
}

func writeServeDescriptor(t *testing.T, root, app, buildID string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, ".ocel", "output", "apps", app, edge.ServeDescriptorFile),
		`{"runtime":"node","buildId":"`+buildID+`"}`)
}
