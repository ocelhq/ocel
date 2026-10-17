package cloud_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/cloud"
	"github.com/ocelhq/ocel/cli/internal/console"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	cloudToken  = "cloud-token"
	session     = "console-session"
	connectorID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"
)

type fakeCloud struct {
	cloudv1connect.UnimplementedAppServiceHandler
	cloudv1connect.UnimplementedDeployServiceHandler
	cloudv1connect.UnimplementedConnectorServiceHandler

	mu          sync.Mutex
	bearers     []string
	created     []string
	installed   []string
	uploads     int
	uploaded    map[string][]byte
	deployed    []*cloudv1.DeployRequest
	deployFail  error
	claimFails  []error
	uploadFails []int
	servedBy    string
}

func (f *fakeCloud) note(header http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bearers = append(f.bearers, header.Get("Authorization"))
}

func (f *fakeCloud) CreateApp(_ context.Context, req *cloudv1.CreateAppRequest) (*cloudv1.CreateAppResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req.GetName())
	if len(f.claimFails) > 0 {
		failed := f.claimFails[0]
		f.claimFails = f.claimFails[1:]
		return nil, failed
	}
	return &cloudv1.CreateAppResponse{App: &cloudv1.App{Name: req.GetName(), ConnectorId: f.servedBy}}, nil
}

func (f *fakeCloud) InstallConnector(_ context.Context, req *cloudv1.InstallConnectorRequest) (*cloudv1.InstallConnectorResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = append(f.installed, req.GetConnectorId())
	return &cloudv1.InstallConnectorResponse{Url: "https://connector.cloud.test", PublicKey: "connector-key"}, nil
}

func (f *fakeCloud) Deploy(_ context.Context, req *cloudv1.DeployRequest) (*cloudv1.DeployResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deployed = append(f.deployed, req)
	if f.deployFail != nil {
		return nil, f.deployFail
	}
	return &cloudv1.DeployResponse{
		Deployment: &cloudv1.Deployment{Id: "dep_1", App: req.GetApp(), Outcome: cloudv1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED, Images: req.GetImages()},
		Url:        "https://" + req.GetApp() + ".cloud.test",
		Urls:       map[string]string{"web": "https://" + req.GetApp() + ".cloud.test"},
	}, nil
}

func (f *fakeCloud) upload(w http.ResponseWriter, r *http.Request) {
	f.note(r.Header)
	f.mu.Lock()
	f.uploads++
	var status int
	if len(f.uploadFails) > 0 {
		status, f.uploadFails = f.uploadFails[0], f.uploadFails[1:]
	}
	f.mu.Unlock()
	if status != 0 {
		w.Header().Set("Retry-After", "1")
		http.Error(w, http.StatusText(status), status)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(body)
	if digest := "sha256:" + hex.EncodeToString(sum[:]); digest != r.PathValue("digest") {
		http.Error(w, "the bytes hash to "+digest, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.uploaded[r.PathValue("digest")] = body
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func serveCloud(t *testing.T) (*fakeCloud, string) {
	t.Helper()
	fake := &fakeCloud{uploaded: map[string][]byte{}}
	noting := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			fake.note(req.Header())
			return next(ctx, req)
		}
	}))
	mux := http.NewServeMux()
	mux.Handle(cloudv1connect.NewAppServiceHandler(fake, noting))
	mux.Handle(cloudv1connect.NewDeployServiceHandler(fake, noting))
	mux.Handle(cloudv1connect.NewConnectorServiceHandler(fake, noting))
	mux.HandleFunc("PUT /v1/images/{digest}", fake.upload)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return fake, srv.URL
}

type fakeConsole struct {
	consolev1connect.UnimplementedConnectorServiceHandler

	mu        sync.Mutex
	existing  *consolev1.Connector
	upserted  []*consolev1.UpsertConnectorRequest
	addressed []*consolev1.SetConnectorAddressRequest
	issued    int
	lifetime  time.Duration
}

func (f *fakeConsole) List(context.Context, *consolev1.ListConnectorsRequest) (*consolev1.ListConnectorsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existing == nil {
		return &consolev1.ListConnectorsResponse{}, nil
	}
	return &consolev1.ListConnectorsResponse{Connectors: []*consolev1.Connector{f.existing}}, nil
}

func (f *fakeConsole) Upsert(_ context.Context, req *consolev1.UpsertConnectorRequest) (*consolev1.UpsertConnectorResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserted = append(f.upserted, req)
	return &consolev1.UpsertConnectorResponse{Connector: &consolev1.Connector{Id: connectorID, Target: req.GetTarget(), Vendor: req.GetVendor()}}, nil
}

func (f *fakeConsole) SetAddress(_ context.Context, req *consolev1.SetConnectorAddressRequest) (*consolev1.SetConnectorAddressResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addressed = append(f.addressed, req)
	return &consolev1.SetConnectorAddressResponse{Connector: &consolev1.Connector{Id: req.GetId(), Url: proto.String(req.GetUrl()), PublicKey: req.PublicKey}}, nil
}

func serveConsole(t *testing.T) (*fakeConsole, *console.Client) {
	t.Helper()
	fake := &fakeConsole{lifetime: 5 * time.Minute}
	path, handler := consolev1connect.NewConnectorServiceHandler(fake)
	mux := http.NewServeMux()
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	mux.HandleFunc("POST /api/cloud/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+session {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fake.mu.Lock()
		fake.issued++
		expiresAt := time.Now().Add(fake.lifetime)
		fake.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"token": cloudToken, "expiresAt": expiresAt})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return fake, console.New(srv.URL)
}

func anImage(t *testing.T, content string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web.tar")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return path, "sha256:" + hex.EncodeToString(sum[:])
}

func shopManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "shop",
		Apps: []*contractv1.ManifestApp{{
			Name:     "web",
			BuildId:  "0123456789abcdef0123456789abcdef",
			Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{Image: "ocel/shop-web@sha256:abc"}},
		}},
	}
}

func TestADeploySendsTheManifestUnchangedWithEachAppsUploadedImage(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	_, consoleClient := serveConsole(t)
	path, digest := anImage(t, "the web app's image")
	manifest := shopManifest()

	deployed, err := cloud.New(url, consoleClient, session).Deploy(context.Background(), "shop", manifest, map[string]string{"web": path}, io.Discard)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if len(ocelCloud.created) != 1 || ocelCloud.created[0] != "shop" {
		t.Errorf("CreateApp = %v, want the app shop claimed", ocelCloud.created)
	}
	if string(ocelCloud.uploaded[digest]) != "the web app's image" {
		t.Errorf("uploaded = %v, want the image's bytes at %s", ocelCloud.uploaded, digest)
	}
	if len(ocelCloud.deployed) != 1 {
		t.Fatalf("Deploy calls = %d, want 1", len(ocelCloud.deployed))
	}
	sent := ocelCloud.deployed[0]
	if sent.GetApp() != "shop" || !proto.Equal(sent.GetManifest(), shopManifest()) || len(sent.GetImages()) != 1 || sent.GetImages()["web"] != digest {
		t.Errorf("DeployRequest for app %q with images %v, manifest unchanged %t; want shop's manifest unchanged and web's image %s",
			sent.GetApp(), sent.GetImages(), proto.Equal(sent.GetManifest(), shopManifest()), digest)
	}
	for _, bearer := range ocelCloud.bearers {
		if bearer != "Bearer "+cloudToken {
			t.Errorf("a call to Ocel Cloud carried %q, want the console's Ocel Cloud token", bearer)
		}
	}
	if deployed.URL != "https://shop.cloud.test" || deployed.Images["web"] != digest || deployed.Deployment.GetId() != "dep_1" {
		t.Errorf("Deploy = %+v, want shop's url, web's digest and the deployment", deployed)
	}
}

func TestADeployMakesOcelCloudTheOrganizationsConnectorOnMicroVMCompute(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	consoleFake, consoleClient := serveConsole(t)
	path, _ := anImage(t, "image")

	if _, err := cloud.New(url, consoleClient, session).Deploy(context.Background(), "shop", shopManifest(), map[string]string{"web": path}, io.Discard); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if len(consoleFake.upserted) != 1 || consoleFake.upserted[0].GetTarget() != cloud.ConnectorTarget || consoleFake.upserted[0].GetVendor() != "ocel" {
		t.Errorf("Upsert = %v, want the ocel/cloud connector of vendor ocel", consoleFake.upserted)
	}
	if len(ocelCloud.installed) != 1 || ocelCloud.installed[0] != connectorID {
		t.Errorf("InstallConnector = %v, want the console's connector %s", ocelCloud.installed, connectorID)
	}
	if len(consoleFake.addressed) != 1 {
		t.Fatalf("SetAddress calls = %d, want 1", len(consoleFake.addressed))
	}
	addressed := consoleFake.addressed[0]
	if addressed.GetUrl() != "https://connector.cloud.test" || addressed.GetPublicKey() != "connector-key" || addressed.GetCompute() != consolev1.ComputeKind_COMPUTE_KIND_MICROVM {
		t.Errorf("SetAddress = %v, want Ocel Cloud's connector address and key on microVM compute", addressed)
	}
}

func TestADeployLeavesAConnectorAlreadyAtOcelCloudsAddressAsItIs(t *testing.T) {
	t.Parallel()

	_, url := serveCloud(t)
	consoleFake, consoleClient := serveConsole(t)
	consoleFake.existing = &consolev1.Connector{
		Id: connectorID, Target: cloud.ConnectorTarget, Vendor: "ocel",
		Url: proto.String("https://connector.cloud.test"), PublicKey: proto.String("connector-key"),
	}
	path, _ := anImage(t, "image")

	if _, err := cloud.New(url, consoleClient, session).Deploy(context.Background(), "shop", shopManifest(), map[string]string{"web": path}, io.Discard); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(consoleFake.upserted) != 0 || len(consoleFake.addressed) != 0 {
		t.Errorf("Upsert = %v, SetAddress = %v, want neither: the connector is in place", consoleFake.upserted, consoleFake.addressed)
	}
}

func TestADeployOcelCloudRefusesIsReturned(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	ocelCloud.deployFail = connect.NewError(connect.CodeFailedPrecondition, errors.New("no image for app web"))
	_, consoleClient := serveConsole(t)
	path, _ := anImage(t, "image")

	_, err := cloud.New(url, consoleClient, session).Deploy(context.Background(), "shop", shopManifest(), map[string]string{"web": path}, io.Discard)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Deploy = %v, want Ocel Cloud's refusal", err)
	}
}

func TestAnOcelCloudAppNameOfAtMostFortyCharactersIsAccepted(t *testing.T) {
	t.Parallel()

	if err := cloud.RefuseAppName(strings.Repeat("a", 40)); err != nil {
		t.Fatalf("RefuseAppName(40 characters) = %v, want nil", err)
	}
}

func TestAnOcelCloudAppNameOfMoreThanFortyCharactersIsRefusedAsInvalidConfig(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("a", 41)
	err := cloud.RefuseAppName(name)
	var refused *clierror.Error
	if !errors.As(err, &refused) || refused.Code != clierror.CodeProjectInvalidConfig || refused.Hint != "slug" {
		t.Fatalf("RefuseAppName(41 characters) = %v, want an invalid config error pointing at slug", err)
	}
	if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "40") {
		t.Errorf("RefuseAppName = %q, want it to name the slug and the 40-character cap", err)
	}
}

func deployShop(t *testing.T, url string, consoleClient *console.Client, images map[string]string) (cloud.Deployed, error) {
	t.Helper()
	return cloud.SkipPauses(cloud.New(url, consoleClient, session)).Deploy(context.Background(), "shop", shopManifest(), images, io.Discard)
}

func TestADeployDrawsEveryCallsTokenFromOneTheConsoleIssuedWhileItStaysValid(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	consoleFake, consoleClient := serveConsole(t)
	path, _ := anImage(t, "image")

	if _, err := deployShop(t, url, consoleClient, map[string]string{"web": path}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if consoleFake.issued != 1 {
		t.Errorf("tokens issued = %d for %d calls to Ocel Cloud, want one token while it stays valid", consoleFake.issued, len(ocelCloud.bearers))
	}
}

func TestADeployHasTheConsoleIssueAFreshTokenOnceTheLastIsAboutToExpire(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	consoleFake, consoleClient := serveConsole(t)
	consoleFake.lifetime = 10 * time.Second
	path, _ := anImage(t, "image")

	if _, err := deployShop(t, url, consoleClient, map[string]string{"web": path}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if consoleFake.issued != len(ocelCloud.bearers) {
		t.Errorf("tokens issued = %d for %d calls to Ocel Cloud, want a fresh one for each call", consoleFake.issued, len(ocelCloud.bearers))
	}
}

func TestADeployClaimsTheAppAgainWhileOcelCloudIsUnavailable(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	ocelCloud.claimFails = []error{connect.NewError(connect.CodeUnavailable, errors.New("busy")), connect.NewError(connect.CodeAborted, errors.New("try again"))}
	_, consoleClient := serveConsole(t)
	path, _ := anImage(t, "image")

	if _, err := deployShop(t, url, consoleClient, map[string]string{"web": path}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(ocelCloud.created) != 3 || len(ocelCloud.deployed) != 1 {
		t.Errorf("CreateApp calls = %d, Deploy calls = %d, want the claim tried three times and the deploy sent once", len(ocelCloud.created), len(ocelCloud.deployed))
	}
}

func TestADeployReturnsAClaimOcelCloudRefusesWithoutAskingAgain(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	ocelCloud.claimFails = []error{connect.NewError(connect.CodeResourceExhausted, errors.New("the plan holds 1 app"))}
	_, consoleClient := serveConsole(t)
	path, _ := anImage(t, "image")

	_, err := deployShop(t, url, consoleClient, map[string]string{"web": path})
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("Deploy = %v, want Ocel Cloud's refusal", err)
	}
	if len(ocelCloud.created) != 1 {
		t.Errorf("CreateApp calls = %d, want 1", len(ocelCloud.created))
	}
}

func TestADeployUploadsAnImageAgainWhileOcelCloudThrottlesOrFails(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	ocelCloud.uploadFails = []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}
	_, consoleClient := serveConsole(t)
	path, digest := anImage(t, "the web app's image")

	if _, err := deployShop(t, url, consoleClient, map[string]string{"web": path}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if ocelCloud.uploads != 3 || string(ocelCloud.uploaded[digest]) != "the web app's image" {
		t.Errorf("uploads = %d, uploaded = %v, want the image's bytes stored on the third try", ocelCloud.uploads, ocelCloud.uploaded)
	}
}

func TestADeployReturnsAnUploadOcelCloudRefusesWithoutSendingItAgain(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	ocelCloud.uploadFails = []int{http.StatusBadRequest}
	_, consoleClient := serveConsole(t)
	path, _ := anImage(t, "image")

	if _, err := deployShop(t, url, consoleClient, map[string]string{"web": path}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("Deploy = %v, want Ocel Cloud's 400", err)
	}
	if ocelCloud.uploads != 1 || len(ocelCloud.deployed) != 0 {
		t.Errorf("uploads = %d, Deploy calls = %d, want one upload and no deploy", ocelCloud.uploads, len(ocelCloud.deployed))
	}
}

func TestADeployOcelCloudCouldNotFinishIsNotSentAgain(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	ocelCloud.deployFail = connect.NewError(connect.CodeUnavailable, errors.New("reach shop's host"))
	_, consoleClient := serveConsole(t)
	path, _ := anImage(t, "image")

	_, err := deployShop(t, url, consoleClient, map[string]string{"web": path})
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("Deploy = %v, want Ocel Cloud's answer", err)
	}
	if len(ocelCloud.deployed) != 1 {
		t.Errorf("Deploy calls = %d, want 1: a deploy carries no key that makes sending it twice safe", len(ocelCloud.deployed))
	}
}

func TestADeployLeavesAConnectorOcelCloudAlreadyServesUninstalled(t *testing.T) {
	t.Parallel()

	ocelCloud, url := serveCloud(t)
	ocelCloud.servedBy = connectorID
	consoleFake, consoleClient := serveConsole(t)
	consoleFake.existing = &consolev1.Connector{
		Id: connectorID, Target: cloud.ConnectorTarget, Vendor: "ocel",
		Url: proto.String("https://connector.cloud.test"), PublicKey: proto.String("connector-key"),
	}
	path, _ := anImage(t, "image")

	if _, err := deployShop(t, url, consoleClient, map[string]string{"web": path}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(ocelCloud.installed) != 0 || len(consoleFake.upserted) != 0 || len(consoleFake.addressed) != 0 {
		t.Errorf("InstallConnector = %v, Upsert = %v, SetAddress = %v, want none: Ocel Cloud already serves the console's connector",
			ocelCloud.installed, consoleFake.upserted, consoleFake.addressed)
	}
}
