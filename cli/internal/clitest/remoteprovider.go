package clitest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/cloud"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/environment"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type RemoteCall struct {
	Procedure     string
	Authorization string
	Version       string
	Session       string
}

type FakeRemoteProvider struct {
	URL      string
	Requests *ProviderRequests

	mu       sync.Mutex
	provider *fake.Provider
	serves   string
	tenants  map[string]string
	sessions map[string]remoteSession
	calls    []RemoteCall
	protos   []int
}

type remoteSession struct {
	tenant  string
	handler http.Handler
}

func ServeRemoteProvider(t *testing.T, p *fake.Provider) *FakeRemoteProvider {
	t.Helper()
	remote := newFakeRemoteProvider(p)
	server := httptest.NewServer(remote.Requests.record(http.HandlerFunc(remote.serve)))
	t.Cleanup(server.Close)
	remote.URL = server.URL
	return remote
}

func ServeRemoteProviderOverHTTP2(t *testing.T, p *fake.Provider) (*FakeRemoteProvider, *http.Client) {
	t.Helper()
	remote := newFakeRemoteProvider(p)
	server := httptest.NewUnstartedServer(remote.Requests.record(http.HandlerFunc(remote.serve)))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	remote.URL = server.URL
	return remote, server.Client()
}

func newFakeRemoteProvider(p *fake.Provider) *FakeRemoteProvider {
	return &FakeRemoteProvider{
		Requests: &ProviderRequests{},
		provider: p,
		serves:   version.Version,
		tenants:  map[string]string{FakeCloudToken: "org_1"},
		sessions: map[string]remoteSession{},
	}
}

func (f *FakeRemoteProvider) ServeVersion(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serves = v
}

func (f *FakeRemoteProvider) GrantTenant(token, tenant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants[token] = tenant
}

func (f *FakeRemoteProvider) Calls() []RemoteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RemoteCall(nil), f.calls...)
}

func (f *FakeRemoteProvider) ProtocolMajors() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.protos...)
}

func (f *FakeRemoteProvider) serve(w http.ResponseWriter, r *http.Request) {
	call := RemoteCall{
		Procedure:     r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		Version:       r.Header.Get(cloud.VersionHeader),
		Session:       r.Header.Get(cloud.SessionHeader),
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.protos = append(f.protos, r.ProtoMajor)
	session, err := f.admit(call)
	f.mu.Unlock()
	if err != nil {
		_ = connect.NewErrorWriter().Write(w, r, err)
		return
	}
	session.ServeHTTP(w, r)
}

func (f *FakeRemoteProvider) admit(call RemoteCall) (http.Handler, error) {
	tenant, known := f.tenants[strings.TrimPrefix(call.Authorization, "Bearer ")]
	switch {
	case !strings.HasPrefix(call.Authorization, "Bearer ") || !known:
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the token is not one Ocel Cloud issued"))
	case call.Version != f.serves:
		return nil, f.versionUnserved(call.Version)
	case !sessionIDPattern.MatchString(call.Session):
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is no session id", call.Session))
	}
	session, open := f.sessions[call.Session]
	if !open {
		if !strings.HasSuffix(call.Procedure, "/Configure") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("a session opens with Configure"))
		}
		session = remoteSession{tenant: tenant, handler: providerserver.ConformanceMux(providerserver.Config{
			Version: version.Version,
			New: func(context.Context, provider.Settings) (provider.Provider, error) {
				return f.provider, nil
			},
		})}
		f.sessions[call.Session] = session
	}
	if session.tenant != tenant {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this session belongs to another organization"))
	}
	return session.handler, nil
}

func (f *FakeRemoteProvider) versionUnserved(called string) error {
	refused := connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this Ocel Cloud serves ocel %s, and was called by ocel %s", f.serves, called))
	detail, err := connect.NewErrorDetail(&cloudv1.VersionUnserved{Served: []string{f.serves}})
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	refused.AddDetail(detail)
	return refused
}

type CloudProject struct {
	FakeProject
	Cloud  *FakeOcelCloud
	Remote *FakeRemoteProvider
}

func SetUpCloudProject(t *testing.T) CloudProject {
	t.Helper()
	root := filepath.Join(t.TempDir(), FixtureSlug)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	p := fake.NewForProject(fake.Options{})
	Bootstrap(t, p, environment.TierProduction, fake.PartCache, fake.PartImages)
	cloud := ServeOcelCloud(t)
	remote := ServeRemoteProvider(t, p)
	cloud.ProviderURL = remote.URL
	return CloudProject{FakeProject: FakeProject{Root: root, Provider: p, Requests: remote.Requests}, Cloud: cloud, Remote: remote}
}

func (c CloudProject) LogIn(invocation *commands.Invocation) {
	c.Cloud.LogIn(invocation)
}
