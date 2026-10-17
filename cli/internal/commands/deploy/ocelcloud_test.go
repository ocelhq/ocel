package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	ocelCloudConnectorID  = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"
	ocelCloudDeploymentID = "0199c3a2-0000-7000-8000-00000000d001"
)

type fakeOcelCloud struct {
	cloudv1connect.UnimplementedAppServiceHandler
	cloudv1connect.UnimplementedDeployServiceHandler
	cloudv1connect.UnimplementedConnectorServiceHandler

	URL string

	mu       sync.Mutex
	created  []string
	uploaded map[string][]byte
	deployed []*cloudv1.DeployRequest
}

func (f *fakeOcelCloud) CreateApp(_ context.Context, req *cloudv1.CreateAppRequest) (*cloudv1.CreateAppResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req.GetName())
	return &cloudv1.CreateAppResponse{App: &cloudv1.App{Name: req.GetName()}}, nil
}

func (f *fakeOcelCloud) InstallConnector(context.Context, *cloudv1.InstallConnectorRequest) (*cloudv1.InstallConnectorResponse, error) {
	return &cloudv1.InstallConnectorResponse{Url: "https://connector.cloud.test", PublicKey: "connector-key"}, nil
}

func (f *fakeOcelCloud) Deploy(_ context.Context, req *cloudv1.DeployRequest) (*cloudv1.DeployResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deployed = append(f.deployed, req)
	url := "https://" + req.GetApp() + ".cloud.test"
	return &cloudv1.DeployResponse{
		Deployment: &cloudv1.Deployment{Id: ocelCloudDeploymentID, App: req.GetApp(), Outcome: cloudv1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED},
		Url:        url,
		Urls:       map[string]string{"web": url},
	}, nil
}

func serveOcelCloud(t *testing.T) *fakeOcelCloud {
	t.Helper()
	fake := &fakeOcelCloud{uploaded: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.Handle(cloudv1connect.NewAppServiceHandler(fake))
	mux.Handle(cloudv1connect.NewDeployServiceHandler(fake))
	mux.Handle(cloudv1connect.NewConnectorServiceHandler(fake))
	mux.HandleFunc("PUT /v1/images/{digest}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.uploaded[r.PathValue("digest")] = body
		fake.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fake.URL = srv.URL
	return fake
}

type fakeOcelCloudConsole struct {
	consolev1connect.UnimplementedConnectorServiceHandler
	consolev1connect.UnimplementedDeploymentServiceHandler

	URL string

	mu      sync.Mutex
	reports []*consolev1.ReportRequest
}

func (f *fakeOcelCloudConsole) List(context.Context, *consolev1.ListConnectorsRequest) (*consolev1.ListConnectorsResponse, error) {
	return &consolev1.ListConnectorsResponse{}, nil
}

func (f *fakeOcelCloudConsole) Upsert(_ context.Context, req *consolev1.UpsertConnectorRequest) (*consolev1.UpsertConnectorResponse, error) {
	return &consolev1.UpsertConnectorResponse{Connector: &consolev1.Connector{Id: ocelCloudConnectorID, Target: req.GetTarget(), Vendor: req.GetVendor()}}, nil
}

func (f *fakeOcelCloudConsole) SetAddress(_ context.Context, req *consolev1.SetConnectorAddressRequest) (*consolev1.SetConnectorAddressResponse, error) {
	return &consolev1.SetConnectorAddressResponse{Connector: &consolev1.Connector{Id: req.GetId()}}, nil
}

func (f *fakeOcelCloudConsole) Report(_ context.Context, req *consolev1.ReportRequest) (*consolev1.ReportResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, req)
	return &consolev1.ReportResponse{}, nil
}

func serveOcelCloudConsole(t *testing.T, offered *fakeOcelCloud) *fakeOcelCloudConsole {
	t.Helper()
	fake := &fakeOcelCloudConsole{}
	mux := http.NewServeMux()
	for _, route := range []func() (string, http.Handler){
		func() (string, http.Handler) { return consolev1connect.NewConnectorServiceHandler(fake) },
		func() (string, http.Handler) { return consolev1connect.NewDeploymentServiceHandler(fake) },
	} {
		path, handler := route()
		mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	}
	mux.HandleFunc("GET /api/cloud", func(w http.ResponseWriter, _ *http.Request) {
		if offered == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not_offered", "error_description": "this console offers no Ocel Cloud"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": offered.URL})
	})
	mux.HandleFunc("POST /api/cloud/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "cloud-token"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fake.URL = srv.URL
	return fake
}

type ocelCloudProject struct {
	root     string
	builds   int
	imageFor map[string]string
}

func setUpOcelCloudProject(t *testing.T, dependencies *Dependencies, slug string) *ocelCloudProject {
	t.Helper()
	p := &ocelCloudProject{root: t.TempDir(), imageFor: map[string]string{}}
	clitest.WriteFile(t, filepath.Join(p.root, "ocel.json"), `{
  "slug": "`+slug+`",
  "apps": [{ "name": "web", "path": "apps/web", "compute": { "container": {} } }]
}
`)
	clitest.WriteFile(t, filepath.Join(p.root, "apps", "web", "Dockerfile"), "FROM scratch\n")
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, archs map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		p.builds++
		if archs["web"] != "amd64" {
			t.Errorf("web is built for %q, want amd64, the architecture Ocel Cloud runs", archs["web"])
		}
		return build.Output{Images: map[string]string{"web": clitest.FixtureImage("web")}}, nil
	}
	stubRecordedBuildIDs(dependencies)
	dependencies.SaveImage = func(_ context.Context, repository, digest string, w io.Writer) error {
		_, err := io.WriteString(w, "image "+repository+"@"+digest)
		return err
	}
	return p
}

func deployWithoutProvider(t *testing.T, dependencies Dependencies, root string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	return stdout.String() + stderr.String(), err
}

func requireBothWaysExplained(t *testing.T, err error) {
	t.Helper()
	var refused *clierror.Error
	if !errors.As(err, &refused) || refused.Code != clierror.CodeProjectInvalidConfig || refused.Hint != "provider" {
		t.Fatalf("runDeploy = %v, want an invalid config error pointing at provider", err)
	}
	for _, said := range []string{`"provider"`, "ocel login", "Ocel Cloud"} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("runDeploy = %q, want it to say %s", err, said)
		}
	}
}

func TestADeployWithNoProviderWhileLoggedOutExplainsBothWaysToDeploy(t *testing.T) {
	dependencies := newTestDependencies()
	p := setUpOcelCloudProject(t, &dependencies, "shop")

	_, err := deployWithoutProvider(t, dependencies, p.root)

	requireBothWaysExplained(t, err)
	if p.builds != 0 {
		t.Errorf("built %d times, want nothing built for a deploy with nowhere to go", p.builds)
	}
}

func TestADeployWithNoProviderToAConsoleThatOffersNoOcelCloudExplainsBothWaysToDeploy(t *testing.T) {
	dependencies := newTestDependencies()
	p := setUpOcelCloudProject(t, &dependencies, "shop")
	consoleFake := serveOcelCloudConsole(t, nil)
	dependencies.Console = clitest.SignedInTo(consoleFake.URL)

	_, err := deployWithoutProvider(t, dependencies, p.root)

	requireBothWaysExplained(t, err)
	if !strings.Contains(err.Error(), consoleFake.URL) {
		t.Errorf("runDeploy = %q, want it to name the console that offers no Ocel Cloud", err)
	}
	if p.builds != 0 {
		t.Errorf("built %d times, want nothing built for a deploy with nowhere to go", p.builds)
	}
}

func TestADeployWithNoProviderToAConsoleThatOffersOcelCloudDeploysTheProjectThere(t *testing.T) {
	dependencies := newTestDependencies()
	p := setUpOcelCloudProject(t, &dependencies, "shop")
	ocelCloud := serveOcelCloud(t)
	dependencies.Console = clitest.SignedInTo(serveOcelCloudConsole(t, ocelCloud).URL)

	said, err := deployWithoutProvider(t, dependencies, p.root)
	if err != nil {
		t.Fatalf("runDeploy: %v\n%s", err, said)
	}

	if len(ocelCloud.created) != 1 || ocelCloud.created[0] != "shop" {
		t.Errorf("CreateApp = %v, want the project's slug claimed", ocelCloud.created)
	}
	if len(ocelCloud.deployed) != 1 {
		t.Fatalf("Deploy calls = %d, want 1", len(ocelCloud.deployed))
	}
	sent := ocelCloud.deployed[0]
	image := []byte("image " + clitest.FixtureImage("web"))
	sum := sha256.Sum256(image)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if sent.GetImages()["web"] != digest || !bytes.Equal(ocelCloud.uploaded[digest], image) {
		t.Errorf("images = %v, want web's saved image uploaded and named by its digest %s", sent.GetImages(), digest)
	}
	manifest := sent.GetManifest()
	var webImage string
	if apps := manifest.GetApps(); len(apps) > 0 {
		webImage = apps[0].GetContainer().GetImage()
	}
	if manifest.GetSlug() != "shop" || len(manifest.GetApps()) != 1 || webImage != clitest.FixtureImage("web") {
		t.Errorf("manifest of %q with %d apps, the first's image %q; want the project's own manifest with web's built image",
			manifest.GetSlug(), len(manifest.GetApps()), webImage)
	}
	if !strings.Contains(said, "https://shop.cloud.test") {
		t.Errorf("output = %q, want it to name where the project serves", said)
	}
}

func TestADeployToOcelCloudRefusesASlugOverFortyCharactersBeforeBuilding(t *testing.T) {
	dependencies := newTestDependencies()
	slug := strings.Repeat("s", 41)
	p := setUpOcelCloudProject(t, &dependencies, slug)
	ocelCloud := serveOcelCloud(t)
	dependencies.Console = clitest.SignedInTo(serveOcelCloudConsole(t, ocelCloud).URL)

	_, err := deployWithoutProvider(t, dependencies, p.root)

	var refused *clierror.Error
	if !errors.As(err, &refused) || refused.Code != clierror.CodeProjectInvalidConfig || !strings.Contains(err.Error(), "40") {
		t.Fatalf("runDeploy = %v, want the slug refused for Ocel Cloud's 40-character cap", err)
	}
	if p.builds != 0 || len(ocelCloud.created) != 0 {
		t.Errorf("built %d times and claimed %v, want neither", p.builds, ocelCloud.created)
	}
}

func TestADeployToOcelCloudIsReportedToTheConsoleOnMicroVMCompute(t *testing.T) {
	dependencies := newTestDependencies()
	p := setUpOcelCloudProject(t, &dependencies, "shop")
	ocelCloud := serveOcelCloud(t)
	consoleFake := serveOcelCloudConsole(t, ocelCloud)
	dependencies.Console = clitest.SignedInTo(consoleFake.URL)
	clitest.LinkToConsole(t, p.root, consoleFake.URL)

	if said, err := deployWithoutProvider(t, dependencies, p.root); err != nil {
		t.Fatalf("runDeploy: %v\n%s", err, said)
	}

	if len(consoleFake.reports) != 1 {
		t.Fatalf("reports = %d, want the deploy reported once", len(consoleFake.reports))
	}
	report := consoleFake.reports[0]
	if err := protovalidate.Validate(report); err != nil {
		t.Errorf("the report is one the console refuses: %v", err)
	}
	deployment := report.GetDeployment()
	if deployment.GetProvider().GetName() != "ocel" || deployment.GetTarget() != "ocel/cloud" || deployment.GetOutcome() != consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED {
		t.Errorf("deployment = %v, want a succeeded deploy through ocel to ocel/cloud", deployment)
	}
	if len(deployment.GetApps()) != 1 {
		t.Fatalf("apps = %v, want web alone", deployment.GetApps())
	}
	got := proto.Clone(deployment.GetApps()[0]).(*consolev1.App)
	release, err := provider.ParseRelease(got.GetRelease())
	if err != nil || release.BuildID() != recordedBuildID("web") {
		t.Errorf("web's release = %q (%v), want a release of web's build %s", got.GetRelease(), err, recordedBuildID("web"))
	}
	if got.GetRelease() == ocelCloudDeploymentID {
		t.Errorf("web's release = %q, the whole deployment's id, want web's own release", got.GetRelease())
	}
	got.Release = ""
	want := &consolev1.App{
		Name:    "web",
		Compute: consolev1.ComputeKind_COMPUTE_KIND_MICROVM,
		BuildId: recordedBuildID("web"),
		Outcome: consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED,
		Urls:    []string{"https://shop.cloud.test"},
	}
	if !proto.Equal(got, want) {
		t.Errorf("web = %v, want %v", got, want)
	}
}
