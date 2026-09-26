package gcp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

func containerStackDeclaring(class edge.Class, env string, values provider.AppValues) provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Class:   class,
			Name:    naming.StackName{Env: env, App: "api"},
		},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:             "api",
			Compute:         provider.ComputeContainer,
			Image:           "europe-west1-docker.pkg.dev/acme/ocel/api@sha256:abc",
			HealthCheckPath: "/healthz",
			Values:          values,
		},
	}
}

func revisionEnv(t *testing.T, server *runServer, spec provider.StackSpec) (*Provider, map[string]string) {
	t.Helper()
	p := server.open(t)
	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	env := map[string]string{}
	for _, entry := range server.standing().Template.Containers[0].Env {
		env[entry.Name] = entry.Value
	}
	return p, env
}

func TestAContainerDeclaringASecretIsHandedAManifestRatherThanThePlaintext(t *testing.T) {
	t.Parallel()
	p, env := revisionEnv(t, &runServer{}, containerStackDeclaring(edge.ClassProduction, "production", provider.AppValues{
		Secrets:      []provider.SecretRef{{Key: "DATABASE_URL"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		ContainerEnv: map[string]string{"REGION": "eu"},
	}))

	for name, value := range env {
		if name == "DATABASE_URL" || name == "SESSION_SECRET" || strings.Contains(value, "postgres://") {
			t.Errorf("the revision carries %s=%q: a secret's plaintext is readable by anyone who may describe the service, so the runtime reads it live instead", name, value)
		}
	}
	if env["REGION"] != "eu" {
		t.Errorf("the revision carries REGION=%q, want the plain value the deploy delivered", env["REGION"])
	}
	if env[originguard.HealthPathVar] != "/healthz" {
		t.Errorf("the revision carries %s=%q, want the probe path so the runtime lets Cloud Run's probe through", originguard.HealthPathVar, env[originguard.HealthPathVar])
	}
	manifest, err := live.Parse([]byte(env[live.EnvVar]))
	if err != nil {
		t.Fatalf("%s = %q, which the runtime cannot read: %v", live.EnvVar, env[live.EnvVar], err)
	}
	if manifest.Project != "acme-prod" || manifest.Region != "europe-west1" || manifest.Namespace != "ocel" ||
		manifest.Slug != "shop" || manifest.Class != "production" || manifest.Environment != "" {
		t.Errorf("manifest = %+v, want the database, key ring and cells named by project, region, namespace, slug and class, with no environment in production", manifest)
	}
	if len(manifest.Keys) != 2 || manifest.Keys[0].Key != "DATABASE_URL" || manifest.Keys[1].Key != "SESSION_SECRET" || manifest.Keys[1].Folder != "/web" {
		t.Errorf("manifest pins %+v, want each secret by key and folder", manifest.Keys)
	}
	if manifest.Endpoint != p.endpoint {
		t.Errorf("the manifest names the endpoint %q, want %q: the runtime reads through the emulator the provider itself talks to, and through Google alone otherwise", manifest.Endpoint, p.endpoint)
	}
}

func TestAPreviewContainerReadsItsOwnEnvironmentsValues(t *testing.T) {
	t.Parallel()
	_, env := revisionEnv(t, &runServer{}, containerStackDeclaring(edge.ClassPreview, "pr-7", provider.AppValues{
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

func TestAContainerWithNothingLiveBootsWithNoManifest(t *testing.T) {
	t.Parallel()
	_, env := revisionEnv(t, &runServer{}, containerStackDeclaring(edge.ClassProduction, "production", provider.AppValues{
		ContainerEnv: map[string]string{"REGION": "eu"},
	}))

	if held, carried := env[live.EnvVar]; carried {
		t.Errorf("the revision carries %s=%q, and a container with no secret opens no store", live.EnvVar, held)
	}
	if env[originguard.HealthPathVar] != "/healthz" {
		t.Errorf("the revision carries %s=%q, want the probe path whether or not anything is live", originguard.HealthPathVar, env[originguard.HealthPathVar])
	}
}

func TestTheProviderWrapsEveryContainerInTheRuntimeItCarries(t *testing.T) {
	t.Parallel()
	p := pushing(t, "")

	if _, err := p.Runtime().Arch(context.Background(), "web", arch.ARM64); err == nil || !strings.Contains(err.Error(), "web") {
		t.Errorf("ContainerArch(arm64) = %v, want the app refused by name before its image is built: Cloud Run runs x86_64 alone", err)
	}
	runs, err := p.Runtime().Arch(context.Background(), "web", "")
	if err != nil || runs != payloads.ContainerArch {
		t.Fatalf("ContainerArch() = %q, %v, want the %s Cloud Run runs: the image is built for whatever this names", runs, err, payloads.ContainerArch)
	}
	held, err := p.Runtime().Binary(context.Background(), runs)
	if err != nil {
		t.Fatalf("ContainerRuntime(%s) = %v", runs, err)
	}
	if want, _ := payloads.ContainerRuntime(payloads.ContainerArch); !bytes.Equal(held, want) {
		t.Error("ContainerRuntime() hands back something other than the embedded payload")
	}
	if _, err := p.Runtime().Binary(context.Background(), "arm64"); err == nil {
		t.Error("ContainerRuntime(arm64) = nil, want a refusal: Cloud Run runs x86_64 alone")
	}
}
