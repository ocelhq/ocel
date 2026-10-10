package providerserver_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func serverlessApp(name, framework string) *contractv1.ManifestApp {
	return &contractv1.ManifestApp{
		Name:      name,
		Framework: &contractv1.Framework{Name: framework},
		Artifact:  &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}},
	}
}

func requiredFeatures(t *testing.T, root string, apps ...*contractv1.ManifestApp) []string {
	t.Helper()
	uses, err := providerserver.UsesOf(root, &contractv1.Manifest{Apps: apps})
	if err != nil {
		t.Fatalf("UsesOf: %v", err)
	}
	features, err := bootstrapplan.RequiredFeatures(fake.NewBootstrap().Catalogue(), uses, "")
	if err != nil {
		t.Fatalf("RequiredFeatures: %v", err)
	}
	return features
}

func TestAContainerNextAppRequiresNoFeatureAServerlessNextAppDoes(t *testing.T) {
	t.Parallel()

	container := &contractv1.ManifestApp{
		Name:      "web",
		Framework: &contractv1.Framework{Name: "next"},
		Artifact:  &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{Image: "ocel/web@sha256:1"}},
	}
	root := t.TempDir()

	if got := requiredFeatures(t, root, serverlessApp("site", "next"), container); !slices.Contains(got, fake.FeatureCache) {
		t.Errorf("features of a serverless and a container Next app = %v, want %q required for the functions", got, fake.FeatureCache)
	}
	if got := requiredFeatures(t, root, container); len(got) != 0 {
		t.Errorf("features of a container-only Next app = %v, want none: what a build uses is served to its functions", got)
	}
}

func TestAnUnbuiltNextAppIsPresumedToUseEveryFeatureNextCanUse(t *testing.T) {
	t.Parallel()

	got := requiredFeatures(t, t.TempDir(), serverlessApp("site", "next"))
	if want := []string{fake.FeatureCache, fake.FeatureImages}; !slices.Equal(got, want) {
		t.Errorf("features of an unbuilt Next app = %v, want %v", got, want)
	}
}

func TestABuiltAppRequiresWhatItsBuildOutputUsesWhateverItsFramework(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		framework   string
		hosting     buildoutput.Hosting
		imageConfig bool
		want        []string
	}{
		{
			name:      "a Next build that declares no ISR and writes no image config",
			framework: "next",
			hosting:   buildoutput.Hosting{Framework: "next"},
		},
		{
			name:        "a Next build that writes an image config but declares no ISR",
			framework:   "next",
			hosting:     buildoutput.Hosting{Framework: "next"},
			imageConfig: true,
			want:        []string{fake.FeatureCache, fake.FeatureImages},
		},
		{
			name:      "a SvelteKit build that declares ISR",
			framework: "sveltekit",
			hosting:   buildoutput.Hosting{Framework: "sveltekit", ISR: true},
			want:      []string{fake.FeatureCache},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := servingRoot(t, "site", tc.hosting, nil)
			if tc.imageConfig {
				if err := os.WriteFile(filepath.Join(buildoutput.AppRoot(root, "site"), naming.ImageConfigFile), []byte(`{}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := requiredFeatures(t, root, serverlessApp("site", tc.framework)); !slices.Equal(got, tc.want) {
				t.Errorf("features = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestADeployWhoseBuildDeclaresISRIsRefusedByABootstrapLackingTheFeatureThatServesIt(t *testing.T) {
	builtApps(t, "web")
	declaresISR(t, "web", buildoutput.FrameworkSvelteKit)
	client, _ := contractServed(t, "1.0.0")
	direct := &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	req := deployRequest()
	req.Edge = direct
	app := req.GetManifest().GetApps()[0]
	app.Framework = &contractv1.Framework{Name: buildoutput.FrameworkSvelteKit}
	app.GetServerless().GetFunctions()[0].Framework = app.GetFramework()

	result, _ := deploy(t, client, req)
	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused for the ISR feature its build declares")
	}
	if want := "lacks the features this project needs: " + fake.FeatureCache; !strings.Contains(result.GetError(), want) {
		t.Errorf("Deploy() error = %q, want it to name %q", result.GetError(), want)
	}
}

func declaresISR(t *testing.T, app, framework string) {
	t.Helper()
	dir := buildoutput.AppRoot(workingOutputRoot(t), app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(buildoutput.Hosting{Version: buildoutput.HostingVersion, Framework: framework, ISR: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
