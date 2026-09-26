package gcp

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/live"
)

func functionStackDeclaring(class edge.Class, env string, values provider.AppValues) provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Class:   class,
			Name:    naming.StackName{Env: env, App: "api"},
		},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:     "api",
			Compute: provider.ComputeServerless,
			Values:  values,
			Functions: []provider.FunctionSpec{{
				Name:      "fn--api--index",
				Image:     "europe-west1-docker.pkg.dev/acme/ocel/api-index@sha256:abc",
				Framework: appbuild.Framework{Name: appbuild.FrameworkNode, Arch: arch.X8664},
				Env:       map[string]string{"OCEL_ROUTE": "index"},
			}},
		},
	}
}

func functionRevisionEnv(t *testing.T, server *runServer, spec provider.StackSpec) (*Provider, map[string]string) {
	t.Helper()
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := map[string]string{}
	for _, entry := range server.standing().Template.Containers[0].Env {
		env[entry.Name] = entry.Value
	}
	return p, env
}

func TestAFunctionDeclaringASecretIsHandedAManifestRatherThanThePlaintext(t *testing.T) {
	t.Parallel()
	p, env := functionRevisionEnv(t, &runServer{}, functionStackDeclaring(edge.ClassProduction, "production", provider.AppValues{
		Secrets:   []provider.SecretRef{{Key: "DATABASE_URL"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		Delivered: map[string]string{"REGION": "eu"},
	}))

	for name, value := range env {
		if name == "DATABASE_URL" || name == "SESSION_SECRET" || strings.Contains(value, "postgres://") {
			t.Errorf("the revision carries %s=%q: a secret's plaintext is readable by anyone who may describe the service, so the runtime reads it live instead", name, value)
		}
	}
	if env["REGION"] != "eu" || env["OCEL_ROUTE"] != "index" {
		t.Errorf("the revision carries REGION=%q and OCEL_ROUTE=%q, want the plain value the deploy delivered and what the function's own spec names", env["REGION"], env["OCEL_ROUTE"])
	}
	if held, carried := env[originguard.HealthPathVar]; carried {
		t.Errorf("the revision carries %s=%q, and a function has no probe path for the runtime to let through", originguard.HealthPathVar, held)
	}
	manifest, err := live.Parse([]byte(env[live.EnvVar]))
	if err != nil {
		t.Fatalf("%s = %q, which the runtime cannot read: %v", live.EnvVar, env[live.EnvVar], err)
	}
	if manifest.Project != "acme-prod" || manifest.Region != "europe-west1" || manifest.Namespace != "ocel" ||
		manifest.Slug != "shop" || manifest.Class != "production" || manifest.Environment != "" {
		t.Errorf("manifest = %+v, want the same database, key ring and cells a container is pointed at", manifest)
	}
	if len(manifest.Keys) != 2 || manifest.Keys[0].Key != "DATABASE_URL" || manifest.Keys[1].Key != "SESSION_SECRET" || manifest.Keys[1].Folder != "/web" {
		t.Errorf("manifest pins %+v, want each secret by key and folder", manifest.Keys)
	}
	if manifest.Endpoint != p.endpoint {
		t.Errorf("the manifest names the endpoint %q, want %q", manifest.Endpoint, p.endpoint)
	}
}

func TestAPreviewFunctionReadsItsOwnEnvironmentsValues(t *testing.T) {
	t.Parallel()
	_, env := functionRevisionEnv(t, &runServer{}, functionStackDeclaring(edge.ClassPreview, "pr-7", provider.AppValues{
		Secrets: []provider.SecretRef{{Key: "MARK"}},
	}))

	manifest, err := live.Parse([]byte(env[live.EnvVar]))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Class != "preview" || manifest.Environment != "pr-7" {
		t.Errorf("manifest = %+v, want the preview's environment so its own value shadows the class-wide one", manifest)
	}
}

func TestAFunctionWithNothingLiveBootsWithNoManifest(t *testing.T) {
	t.Parallel()
	_, env := functionRevisionEnv(t, &runServer{}, functionStackDeclaring(edge.ClassProduction, "production", provider.AppValues{
		Delivered: map[string]string{"REGION": "eu"},
	}))

	if held, carried := env[live.EnvVar]; carried {
		t.Errorf("the revision carries %s=%q, and a function with no secret opens no store", live.EnvVar, held)
	}
}

func TestTheProviderBakesNothingIntoAFunctionRevision(t *testing.T) {
	t.Parallel()
	p := pushing(t, "")

	if p.Hooks().FunctionImages == nil {
		t.Fatal("the provider builds no function image, and only one it builds can be wrapped in its runtime")
	}
}
