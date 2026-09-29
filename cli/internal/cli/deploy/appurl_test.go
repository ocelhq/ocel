package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/processenv"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func manifestVariable(t *testing.T, manifest *contractv1.Manifest, app, key string) *contractv1.ManifestVariable {
	t.Helper()
	for _, a := range manifest.GetApps() {
		if a.GetName() != app {
			continue
		}
		for _, v := range a.GetVariables() {
			if v.GetKey() == key {
				return v
			}
		}
		keys := make([]string, 0, len(a.GetVariables()))
		for _, v := range a.GetVariables() {
			keys = append(keys, v.GetKey())
		}
		t.Fatalf("app %q has no %s among its variables %q", app, key, keys)
	}
	t.Fatalf("manifest has no app %q", app)
	return nil
}

func TestTheDeploymentURLReachesEveryDeliverySite(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	deps := clitest.NewDeps()
	clitest.StubRecordedDeploymentIDs(&deps)

	var built map[string]map[string]string
	deps.BuildApps = func(_ context.Context, cfg *project.Project, env map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
		built = env
		return functionsOnDisk(&deps, cfg)
	}

	s, _ := newBuildSpan(t)
	cfg := prebuiltConfig(root)
	urls := map[string]string{"api": "https://api.acme.com"}
	manifest, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), phase: s, span: s, urls: urls})
	if err != nil {
		t.Fatalf("collectBuildAndAssemble: %v", err)
	}

	t.Run("the build is handed it", func(t *testing.T) {
		if got, want := built["api"][processenv.AppURLEnvVar], "https://api.acme.com"; got != want {
			t.Errorf("build env = %v, want %s = %q", built["api"], processenv.AppURLEnvVar, want)
		}
		if got, want := built["api"][appbuild.ClientURLEnvName], "https://api.acme.com"; got != want {
			t.Errorf("build env = %v, want %s = %q for the browser bundle", built["api"], appbuild.ClientURLEnvName, want)
		}
	})

	t.Run("the manifest passes it to the provider", func(t *testing.T) {
		if got, want := manifestVariable(t, manifest, "api", processenv.AppURLEnvVar).GetValue(), "https://api.acme.com"; got != want {
			t.Errorf("%s = %q, want %q", processenv.AppURLEnvVar, got, want)
		}
		if got, want := manifestVariable(t, manifest, "api", appbuild.ClientURLEnvName).GetValue(), "https://api.acme.com"; got != want {
			t.Errorf("%s = %q, want %q", appbuild.ClientURLEnvName, got, want)
		}
	})

	t.Run("the client accessor inlines it", func(t *testing.T) {
		accessor, err := os.ReadFile(filepath.Join(root, statedir.Name, "env-client.ts"))
		if err != nil {
			t.Fatalf("no client accessor was generated: %v", err)
		}
		if !strings.Contains(string(accessor), appbuild.ClientURLEnvName) {
			t.Errorf("accessor = %s, want it to read %s", accessor, appbuild.ClientURLEnvName)
		}
	})
}

func TestPrebuiltRefusesAnOutputBuiltForAnotherURL(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	deps := clitest.NewDeps()
	recordBuildApp(&deps)
	cfg := prebuiltConfig(root)

	s, _ := newBuildSpan(t)
	if _, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), phase: s, span: s, urls: map[string]string{"api": "https://api.acme.com"}}); err != nil {
		t.Fatalf("collectBuildAndAssemble: %v", err)
	}

	s, _ = newBuildSpan(t)
	_, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s, urls: map[string]string{"api": "https://pr-1.preview.acme.com"}})
	if err == nil {
		t.Fatal("collectBuildAndAssemble = nil for output built against another hostname, want a refusal: the url is inlined into the browser bundle, so this deploy would serve the wrong one")
	}
	if !strings.Contains(err.Error(), appbuild.ClientURLEnvName) {
		t.Errorf("error = %q, want it to name the key whose value changed", err)
	}
}
