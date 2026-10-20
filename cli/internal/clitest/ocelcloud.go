package clitest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const (
	FakeCloudToken    = "cloud-token"
	FakeCloudSourceID = "src_fake"
)

type FakeOcelCloud struct {
	ConsoleURL string
	APIURL     string

	consolev1connect.UnimplementedProjectServiceHandler
	consolev1connect.UnimplementedConsoleServiceHandler
	cloudv1connect.UnimplementedUploadServiceHandler

	mu            sync.Mutex
	Organizations []console.Organization
	ActiveOrg     string
	Projects      []*consolev1.Project
	Created       []*consolev1.CreateProjectRequest
	ActivatedOrgs []string
	Blobs         map[string][]byte
	Committed     *cloudv1.SourceManifest
	CloudBearers  []string
}

func ServeOcelCloud(t *testing.T) *FakeOcelCloud {
	t.Helper()
	fake := &FakeOcelCloud{
		Organizations: []console.Organization{{ID: "org_1", Name: "Acme Inc", Slug: "acme"}},
		Blobs:         map[string][]byte{},
	}
	validate := connect.WithInterceptors(connectvalidate.NewInterceptor())

	consoleMux := http.NewServeMux()
	consoleMux.HandleFunc("/api/auth/organization/list", func(w http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_ = json.NewEncoder(w).Encode(fake.Organizations)
	})
	consoleMux.HandleFunc("/api/auth/get-session", func(w http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"session":{"activeOrganizationId":%q},"user":{"email":"dev@acme.test"}}`, fake.ActiveOrg)
	})
	consoleMux.HandleFunc("/api/auth/organization/set-active", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OrganizationID string `json:"organizationId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.ActivatedOrgs = append(fake.ActivatedOrgs, body.OrganizationID)
		fake.mu.Unlock()
		_, _ = w.Write([]byte("{}"))
	})
	for _, mount := range []func() (string, http.Handler){
		func() (string, http.Handler) { return consolev1connect.NewProjectServiceHandler(fake, validate) },
		func() (string, http.Handler) { return consolev1connect.NewConsoleServiceHandler(fake, validate) },
	} {
		path, handler := mount()
		consoleMux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	}
	consoleServer := httptest.NewServer(consoleMux)
	t.Cleanup(consoleServer.Close)
	fake.ConsoleURL = consoleServer.URL

	cloudMux := http.NewServeMux()
	cloudMux.Handle(cloudv1connect.NewUploadServiceHandler(fake, validate))
	cloudMux.HandleFunc("PUT /v1/blobs/{digest}", fake.putBlob)
	cloudServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.CloudBearers = append(fake.CloudBearers, r.Header.Get("Authorization"))
		fake.mu.Unlock()
		cloudMux.ServeHTTP(w, r)
	}))
	t.Cleanup(cloudServer.Close)
	fake.APIURL = cloudServer.URL
	return fake
}

func (f *FakeOcelCloud) LogIn(invocation *commands.Invocation) {
	invocation.Console.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{AccessToken: "device-session", APIURL: f.ConsoleURL, Email: "dev@acme.test"}, nil
	}
	invocation.CloudOffer = func(context.Context, console.Credentials) (*console.CloudOffer, error) {
		return &console.CloudOffer{APIURL: f.APIURL, Audience: "ocel-cloud"}, nil
	}
}

func (f *FakeOcelCloud) List(context.Context, *consolev1.ListProjectsRequest) (*consolev1.ListProjectsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &consolev1.ListProjectsResponse{Projects: f.Projects}, nil
}

func (f *FakeOcelCloud) Create(_ context.Context, req *consolev1.CreateProjectRequest) (*consolev1.CreateProjectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Created = append(f.Created, req)
	created := &consolev1.Project{Id: "0199c3a2-5b7e-7c4d-8a1f-000000000001", OrganizationId: "org_1", Name: req.GetName(), Slug: req.GetSlug()}
	f.Projects = append(f.Projects, created)
	return &consolev1.CreateProjectResponse{Project: created}, nil
}

func (f *FakeOcelCloud) IssueCloudToken(context.Context, *consolev1.IssueCloudTokenRequest) (*consolev1.IssueCloudTokenResponse, error) {
	return &consolev1.IssueCloudTokenResponse{Token: FakeCloudToken, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}, nil
}

func (f *FakeOcelCloud) Missing(_ context.Context, req *cloudv1.MissingRequest) (*cloudv1.MissingResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var missing []string
	for _, digest := range req.GetDigests() {
		if _, held := f.Blobs[digest]; !held {
			missing = append(missing, digest)
		}
	}
	return &cloudv1.MissingResponse{Digests: missing}, nil
}

func (f *FakeOcelCloud) CommitSource(_ context.Context, req *cloudv1.CommitSourceRequest) (*cloudv1.CommitSourceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range req.GetManifest().GetFiles() {
		if _, held := f.Blobs[file.GetDigest()]; file.GetDigest() != "" && !held {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s was never uploaded", file.GetPath()))
		}
	}
	f.Committed = req.GetManifest()
	return &cloudv1.CommitSourceResponse{SourceId: FakeCloudSourceID}, nil
}

func (f *FakeOcelCloud) putBlob(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != r.PathValue("digest") {
		http.Error(w, "the body does not hash to the digest", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Blobs[r.PathValue("digest")] = body
	w.WriteHeader(http.StatusCreated)
}

func (f *FakeOcelCloud) CommittedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var paths []string
	for _, file := range f.Committed.GetFiles() {
		paths = append(paths, file.GetPath())
	}
	return paths
}
