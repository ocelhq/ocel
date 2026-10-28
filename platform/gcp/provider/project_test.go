package gcp_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges"
)

func withoutAnAmbientProject(t *testing.T) {
	t.Helper()
	withoutApplicationDefaultCredentials(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("CLOUDSDK_CORE_PROJECT", "")
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
	t.Setenv("PATH", t.TempDir())
}

func withGcloudNaming(t *testing.T, project string) (ran func() bool) {
	t.Helper()
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(bin, "gcloud"), []byte("#!/bin/sh\ntouch "+marker+"\necho "+project+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}
}

func targeted(t *testing.T, options provider.Options) string {
	t.Helper()
	p, err := gcp.New(context.Background(), provider.Settings{Options: options})
	if err != nil {
		t.Fatalf("New(%v) = %v, want a provider", options, err)
	}
	project, err := p.(*gcp.Provider).Project(context.Background())
	if err != nil {
		t.Fatalf("Project() = %v, want the project the run targets", err)
	}
	return project
}

func TestAProjectNamedInTheOptionsIsTheProjectTheRunTargets(t *testing.T) {
	withoutAnAmbientProject(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "ambient-elsewhere")

	if got := targeted(t, provider.Options{"project": "acme-prod", "region": "europe-west1"}); got != "acme-prod" {
		t.Errorf("the run targets %q, want the project the options named", got)
	}
}

func TestAProjectNobodyNamedIsTakenFromTheEnvironmentTheRunInherits(t *testing.T) {
	withoutAnAmbientProject(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "ambient-prod")

	if got := targeted(t, provider.Options{"region": "europe-west1"}); got != "ambient-prod" {
		t.Errorf("the run targets %q, want the ambient project: switching credentials is how one project deploys production and another previews", got)
	}
}

func TestTheCloudSDKProjectIsReadWhenNothingBeforeItNamesOne(t *testing.T) {
	withoutAnAmbientProject(t)
	t.Setenv("CLOUDSDK_CORE_PROJECT", "sdk-prod")

	if got := targeted(t, provider.Options{"region": "europe-west1"}); got != "sdk-prod" {
		t.Errorf("the run targets %q, want the project CLOUDSDK_CORE_PROJECT names", got)
	}
}

func TestTheAmbientProjectIsReadFromGcloudWhenNothingElseNamesOne(t *testing.T) {
	withoutAnAmbientProject(t)
	withGcloudNaming(t, "gcloud-config-prod")

	if got := targeted(t, provider.Options{"region": "europe-west1"}); got != "gcloud-config-prod" {
		t.Errorf("the run targets %q, want the project gcloud's config names", got)
	}
}

func TestReadingTheProjectFromGcloudIsASpanWithWhatGcloudSaid(t *testing.T) {
	withoutAnAmbientProject(t)
	withGcloudNaming(t, "gcloud-config-prod")
	ctx, spans := tracingtest.Record(t)
	p, err := gcp.New(ctx, provider.Settings{Options: provider.Options{"region": "europe-west1"}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := p.(*gcp.Provider).Project(ctx); err != nil {
		t.Fatal(err)
	}

	ran := spans.Only(t)
	if ran.Name() != "gcloud" {
		t.Fatalf("reading the project made span %q, want gcloud", ran.Name())
	}
	if said := tracingtest.Attributes(ran)["process.stdout.tail"]; !strings.Contains(said.AsString(), "gcloud-config-prod") {
		t.Errorf("the gcloud span recorded stdout %q, want the project it named", said.AsString())
	}
}

func TestTheCloudflareOriginIsTheAmbientProjectWhenTheOptionsNameNone(t *testing.T) {
	withoutAnAmbientProject(t)
	withGcloudNaming(t, "gcloud-config-prod")

	p, err := gcp.New(context.Background(), provider.Settings{Options: provider.Options{"region": "europe-west1"}})
	if err != nil {
		t.Fatalf("New() = %v, want a provider", err)
	}
	origin, err := p.Edges().(edges.Registry).Cloudflare().Scope(context.Background())
	if err != nil {
		t.Fatalf("Cloudflare().Scope() = %v, want the project gcloud's config names", err)
	}
	if want := (cloudflare.OriginScope{Vendor: "gcp", ID: "gcloud-config-prod"}); origin != want {
		t.Errorf("Cloudflare().Scope() = %v, want %v", origin, want)
	}
}

func TestAProjectNeitherNamedNorAmbientIsRefusedNamingWhereItIsReadWhenTheCloudIsReached(t *testing.T) {
	withoutAnAmbientProject(t)

	p, err := gcp.New(context.Background(), provider.Settings{Options: provider.Options{"region": "europe-west1"}})
	if err != nil {
		t.Fatalf("New() with no project and nothing ambient = %v, want a provider: nothing has reached the cloud yet", err)
	}
	var refused refusal.Refusal
	_, err = p.Credentials().Whoami(context.Background())
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Whoami() with no project and nothing ambient = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	for _, named := range []string{"project", "GOOGLE_CLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT", "gcloud"} {
		if !strings.Contains(refused.Message, named) {
			t.Errorf("Whoami() refused with %q, want it to name %s among the places a project is read from", refused.Message, named)
		}
	}
}

func TestAGcloudThatFailsIsNamedInTheRefusalRatherThanReadAsNamingNothing(t *testing.T) {
	withoutAnAmbientProject(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gcloud"),
		[]byte("#!/bin/sh\necho 'ERROR: (gcloud.config.get-value) the active account is not set' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	p, err := gcp.New(context.Background(), provider.Settings{Options: provider.Options{"region": "europe-west1"}})
	if err != nil {
		t.Fatalf("New() = %v, want a provider: nothing has reached the cloud yet", err)
	}
	var refused refusal.Refusal
	_, err = p.Credentials().Whoami(context.Background())
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Whoami() with a failing gcloud = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, "the active account is not set") {
		t.Errorf("Whoami() refused with %q, want gcloud's own failure in it: a gcloud that fails is not a gcloud that names nothing", refused.Message)
	}
}

func withApplicationDefaultCredentials(t *testing.T, project string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	written, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   project,
		"private_key":  string(block),
		"client_email": "deployer@" + project + ".iam.gserviceaccount.com",
		"token_uri":    "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, written, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
}

func TestAProjectNamedInTheEnvironmentBeatsTheOneTheCredentialsName(t *testing.T) {
	withoutAnAmbientProject(t)
	withApplicationDefaultCredentials(t, "credential-project")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "ambient-prod")

	if got := targeted(t, provider.Options{"region": "europe-west1"}); got != "ambient-prod" {
		t.Errorf("the run targets %q, want the project the environment names: on GCE the credentials always name the metadata project, and naming one is how an operator overrides it", got)
	}
}

func TestTheCloudSDKProjectBeatsTheOneTheCredentialsName(t *testing.T) {
	withoutAnAmbientProject(t)
	withApplicationDefaultCredentials(t, "credential-project")
	t.Setenv("CLOUDSDK_CORE_PROJECT", "sdk-prod")

	if got := targeted(t, provider.Options{"region": "europe-west1"}); got != "sdk-prod" {
		t.Errorf("the run targets %q, want the project CLOUDSDK_CORE_PROJECT names", got)
	}
}

func TestTheCredentialsAreReadWhenNothingAroundTheRunNamesAProject(t *testing.T) {
	withoutAnAmbientProject(t)
	withApplicationDefaultCredentials(t, "credential-project")

	if got := targeted(t, provider.Options{"region": "europe-west1"}); got != "credential-project" {
		t.Errorf("the run targets %q, want the project the credentials name", got)
	}
}

func configured(t *testing.T, options provider.Options) (contractv1connect.ProviderServiceClient, costv1connect.CostServiceClient) {
	t.Helper()
	config := providerserver.Config{
		Version: "test",
		New: func(ctx context.Context, _ provider.Settings) (provider.Provider, error) {
			return gcp.New(ctx, provider.Settings{Options: options})
		},
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{ProjectDir: t.TempDir()}}); err != nil {
		t.Fatalf("Configure(%v) error = %v, want it to answer: nothing has reached the cloud yet", options, err)
	}
	return client, costv1connect.NewCostServiceClient(server.Client(), server.URL)
}

func scanned(t *testing.T, options provider.Options) *costv1.ResourceSet {
	t.Helper()
	client, costs := configured(t, options)
	set, err := client.Shape(context.Background(), &contractv1.ShapeRequest{
		Manifest:    shopManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Shape(%v) = %v, want the shape with no credentials and no gcloud consulted", options, err)
	}
	if _, err := costs.Price(context.Background(), &costv1.PriceRequest{Resources: set}); err != nil {
		t.Fatalf("Price() = %v, want an estimate with no credentials and no gcloud consulted", err)
	}
	return set
}

func TestAScanReadsNoCredentialsAndRunsNoGcloud(t *testing.T) {
	withoutAnAmbientProject(t)
	ran := withGcloudNaming(t, "gcloud-config-prod")

	for _, r := range scanned(t, provider.Options{"project": "acme-prod", "region": "europe-west1"}).GetResources() {
		if strings.Contains(r.GetName(), "gcloud-config-prod") {
			t.Errorf("%s is named for the project gcloud's config names; a scan reads the project it was told and nothing ambient", r.GetName())
		}
	}
	if ran() {
		t.Errorf("the scan ran gcloud; a scan reads nothing ambient")
	}
}

func TestAScanOfADeployWhoseProjectNothingNamesShapesWhatItShapesWithOneNamed(t *testing.T) {
	withoutAnAmbientProject(t)
	ran := withGcloudNaming(t, "gcloud-config-prod")

	named := scanned(t, provider.Options{"project": "acme-prod", "region": "europe-west1"})
	unnamed := scanned(t, provider.Options{"region": "europe-west1"})
	if !proto.Equal(unnamed, named) {
		t.Errorf("the scan with no project named shapes %v, want what it shapes with one named, %v: no name a shape holds carries the project", unnamed, named)
	}
	if ran() {
		t.Errorf("the scan with no project named ran gcloud to find one; a scan reads nothing ambient")
	}
}
