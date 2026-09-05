package deploy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
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
		t.Fatalf("app %q carries no %s: %+v", app, key, a.GetVariables())
	}
	t.Fatalf("manifest carries no app %q", app)
	return nil
}

func TestTheDeploymentURLReachesEveryDeliverySite(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	deps := clitest.NewDeps()
	clitest.StubRecordedDeploymentIDs(&deps)

	var built map[string]map[string]string
	deps.BuildApp = func(_ context.Context, _ *projectconfig.Config, env map[string]map[string]string, _ io.Writer) error {
		built = env
		return nil
	}

	s, _ := newBuildManifestSession(t)
	cfg := prebuiltConfig(root)
	urls := map[string]string{"api": "https://api.acme.com"}
	manifest, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), false, s, "serverless", urls)
	if err != nil {
		t.Fatalf("collectAndBuildManifest: %v", err)
	}

	t.Run("the build is handed it", func(t *testing.T) {
		if got, want := built["api"][providerkit.URLEnvName], "https://api.acme.com"; got != want {
			t.Errorf("build env = %v, want %s = %q", built["api"], providerkit.URLEnvName, want)
		}
		if got, want := built["api"][providerkit.ClientURLEnvName], "https://api.acme.com"; got != want {
			t.Errorf("build env = %v, want %s = %q for the browser bundle", built["api"], providerkit.ClientURLEnvName, want)
		}
	})

	t.Run("the manifest carries it to the provider", func(t *testing.T) {
		if got, want := manifestVariable(t, manifest, "api", providerkit.URLEnvName).GetValue(), "https://api.acme.com"; got != want {
			t.Errorf("%s = %q, want %q", providerkit.URLEnvName, got, want)
		}
		if got, want := manifestVariable(t, manifest, "api", providerkit.ClientURLEnvName).GetValue(), "https://api.acme.com"; got != want {
			t.Errorf("%s = %q, want %q", providerkit.ClientURLEnvName, got, want)
		}
	})

	t.Run("the client accessor inlines it", func(t *testing.T) {
		accessor, err := os.ReadFile(filepath.Join(root, ".ocel", "env-client.ts"))
		if err != nil {
			t.Fatalf("no client accessor was generated: %v", err)
		}
		if !strings.Contains(string(accessor), providerkit.ClientURLEnvName) {
			t.Errorf("accessor = %s, want it to read %s", accessor, providerkit.ClientURLEnvName)
		}
	})
}

func TestPrebuiltRefusesAnOutputBuiltForAnotherURL(t *testing.T) {
	root := t.TempDir()
	clitest.WritePrebuiltFunction(t, root, "api", "index")
	deps := clitest.NewDeps()
	recordBuildApp(&deps)
	cfg := prebuiltConfig(root)

	s, _ := newBuildManifestSession(t)
	if _, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), false, s, "serverless",
		map[string]string{"api": "https://api.acme.com"}); err != nil {
		t.Fatalf("collectAndBuildManifest: %v", err)
	}

	s, _ = newBuildManifestSession(t)
	_, err := collectAndBuildManifest(context.Background(), deps, cfg, noGate(cfg), true, s, "serverless",
		map[string]string{"api": "https://pr-1.preview.acme.com"})
	if err == nil {
		t.Fatal("collectAndBuildManifest = nil for output built against another hostname, want a refusal: the url is inlined into the browser bundle, so this deploy would serve the wrong one")
	}
	if !strings.Contains(err.Error(), providerkit.ClientURLEnvName) {
		t.Errorf("error = %q, want it to name the key whose value changed", err)
	}
}
