package providerprocess_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/cloud"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/version"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type issuedTokens struct {
	mu     sync.Mutex
	tokens []string
	issued int
}

func (s *issuedTokens) Ensure(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := s.tokens[min(s.issued, len(s.tokens)-1)]
	s.issued++
	return token, nil
}

func remoteSpan(t *testing.T) (context.Context, *run.Span) {
	t.Helper()
	ctx, running, err := run.NewBus(time.Now).Begin(t.Context(), "ocel logs", "")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, running.Phase(progressv1.Phase_PHASE_CHECK)
}

func remoteProject(t *testing.T) *project.Project {
	t.Helper()
	dir := t.TempDir()
	return project.NewUnconfigured(dir, "shop")
}

func startRemote(t *testing.T, server *clitest.FakeRemoteProvider, cfg *project.Project, tokens cloud.TokenSource) (*providerprocess.Provider, error) {
	t.Helper()
	ctx, span := remoteSpan(t)
	p, err := providerprocess.StartRemote(ctx, cfg, span, providerprocess.Questions{}, providerprocess.Remote{Name: "ocel", URL: server.URL, Tokens: tokens})
	if p != nil {
		t.Cleanup(p.Close)
	}
	return p, err
}

func listEnvironments(ctx context.Context, p *providerprocess.Provider) error {
	return p.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		_, err := client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: "shop"})
		return err
	})
}

func TestEveryCallToARemoteProviderCarriesTheTokenTheCLIVersionAndTheSessionItWasOpenedWith(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	tokens := &issuedTokens{tokens: []string{clitest.FakeCloudToken}}
	cfg := remoteProject(t)

	first, err := startRemote(t, server, cfg, tokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := listEnvironments(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	store, err := first.VariableStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListValues(t.Context(), &variablestorev1.ListValuesRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: "shop"}); err != nil {
		t.Fatal(err)
	}
	second, err := startRemote(t, server, cfg, tokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := listEnvironments(t.Context(), second); err != nil {
		t.Fatal(err)
	}

	calls := server.Calls()
	if len(calls) != 5 {
		t.Fatalf("calls = %+v, want Configure, two calls, Configure and one call", calls)
	}
	for _, call := range calls {
		if call.Authorization != "Bearer "+clitest.FakeCloudToken || call.Version != version.Version || len(call.Session) != 32 {
			t.Fatalf("call = %+v, want the bearer token, ocel %s and a session id", call, version.Version)
		}
	}
	if calls[0].Session != calls[1].Session || calls[1].Session != calls[2].Session {
		t.Fatalf("calls = %+v, want one session for every call through one open provider", calls)
	}
	if calls[3].Session == calls[0].Session || calls[3].Session != calls[4].Session {
		t.Fatalf("calls = %+v, want a provider opened again to open a session of its own", calls)
	}
	if !strings.HasSuffix(calls[0].Procedure, "/Configure") || !strings.HasSuffix(calls[3].Procedure, "/Configure") {
		t.Fatalf("calls = %+v, want each session opened by Configure", calls)
	}
}

func TestARemoteProviderIsConfiguredWithTheProjectAndAnswersItsFacts(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	cfg := remoteProject(t)

	p, err := startRemote(t, server, cfg, &issuedTokens{tokens: []string{clitest.FakeCloudToken}})
	if err != nil {
		t.Fatal(err)
	}
	configured := clitest.RequestsTo[*contractv1.ConfigureRequest](t, server.Requests, contractv1connect.ProviderServiceConfigureProcedure)
	if len(configured) != 1 {
		t.Fatalf("configured %d times, want once", len(configured))
	}
	config := configured[0].GetConfig()
	if config.GetSlug() != "shop" || config.GetProjectDir() != cfg.Dir || !filepath.IsAbs(config.GetProjectDir()) || len(config.GetTransforms()) != 0 || config.GetOptions() != nil {
		t.Fatalf("config = %v, want the slug, the absolute project dir, and no transforms or options", config)
	}
	if len(p.Facts().GetComputes()) == 0 || p.Name() != "ocel" {
		t.Fatalf("facts = %v, name %q, want the provider's facts under the name it was opened as", p.Facts(), p.Name())
	}
}

func TestARemoteProviderAsksForATokenOnEveryCallSoAnExpiredOneIsReplaced(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	server.GrantTenant("renewed-token", "org_1")
	tokens := &issuedTokens{tokens: []string{clitest.FakeCloudToken, "renewed-token"}}

	p, err := startRemote(t, server, remoteProject(t), tokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := listEnvironments(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	calls := server.Calls()
	if calls[0].Authorization != "Bearer "+clitest.FakeCloudToken || calls[1].Authorization != "Bearer renewed-token" {
		t.Fatalf("calls = %+v, want the second call to carry the token issued for it", calls)
	}
}

func TestARemoteProviderServingAnotherReleaseFailsWithCloudUpgradeRequired(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	tokens := &issuedTokens{tokens: []string{clitest.FakeCloudToken}}

	p, err := startRemote(t, server, remoteProject(t), tokens)
	if err != nil {
		t.Fatal(err)
	}
	server.ServeVersion("9.9.9")
	ctx, _ := remoteSpan(t)
	for name, call := range map[string]func() error{
		"a unary call": func() error { return listEnvironments(ctx, p) },
		"a stream": func() error {
			_, err := providerprocess.Stream(ctx, p, "RemoveEnvironment", &contractv1.RemoveEnvironmentRequest{Slug: "shop", Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "feat-x"}}, contractv1connect.ProviderServiceClient.RemoveEnvironment)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			var coded *clierror.Error
			if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUpgradeRequired || !strings.Contains(err.Error(), "upgrade ocel") || !strings.Contains(err.Error(), "serves ocel 9.9.9") {
				t.Fatalf("err = %v, want cloud.upgrade_required in Ocel Cloud's words", err)
			}
		})
	}

	_, err = startRemote(t, server, remoteProject(t), tokens)
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUpgradeRequired {
		t.Fatalf("open err = %v, want cloud.upgrade_required", err)
	}
}

func TestARemoteProviderRedirectedToAnotherHostIsNeverSentTheToken(t *testing.T) {
	elsewhere := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	target, err := url.Parse(elsewhere.URL)
	if err != nil {
		t.Fatal(err)
	}
	target.Host = "localhost:" + target.Port()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.String()+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	ctx, span := remoteSpan(t)

	_, _ = providerprocess.StartRemote(ctx, remoteProject(t), span, providerprocess.Questions{}, providerprocess.Remote{Name: "ocel", URL: redirecting.URL, Tokens: &issuedTokens{tokens: []string{clitest.FakeCloudToken}}})
	calls := elsewhere.Calls()
	if len(calls) == 0 {
		t.Fatal("the redirect was never followed")
	}
	for _, call := range calls {
		if call.Authorization != "" {
			t.Fatalf("call = %+v, want the other host sent no token", call)
		}
	}
}

func TestARemoteSessionCalledWithAnotherOrganizationsTokenFailsInOcelCloudsWords(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	server.GrantTenant("other-org-token", "org_2")
	tokens := &issuedTokens{tokens: []string{clitest.FakeCloudToken, "other-org-token"}}

	p, err := startRemote(t, server, remoteProject(t), tokens)
	if err != nil {
		t.Fatal(err)
	}
	err = listEnvironments(t.Context(), p)
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "belongs to another organization") {
		t.Fatalf("err = %v, want the session refused for the other organization", err)
	}
}

func TestARemoteProviderRejectingTheTokenFailsWithConsoleNotLoggedIn(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))

	_, err := startRemote(t, server, remoteProject(t), &issuedTokens{tokens: []string{"forged"}})
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeConsoleNotLoggedIn || !strings.Contains(err.Error(), "ocel login") {
		t.Fatalf("err = %v, want console.not_logged_in naming ocel login", err)
	}
}

func TestARemoteProviderOutOfSessionsWhenOneOpensFailsAsBusyInOcelCloudsWords(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodeResourceExhausted, errors.New("cloud.session_limit: this organization holds 64 open sessions")))
	}))
	t.Cleanup(refusing.Close)
	ctx, span := remoteSpan(t)

	_, err := providerprocess.StartRemote(ctx, remoteProject(t), span, providerprocess.Questions{}, providerprocess.Remote{Name: "ocel", URL: refusing.URL, Tokens: &issuedTokens{tokens: []string{clitest.FakeCloudToken}}})
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeProviderBusy || !coded.Retryable {
		t.Fatalf("err = %v, want a retryable %s", err, clierror.CodeProviderBusy)
	}
	if !strings.Contains(err.Error(), "cloud.session_limit: this organization holds 64 open sessions") || strings.Contains(err.Error(), "resource_exhausted") {
		t.Fatalf("err = %q, want Ocel Cloud's own words without the Connect code", err)
	}
}

func TestAVariableStoreCallRefusedByARemoteProviderFailsWithTheCodeOfItsKind(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	forward := httputil.NewSingleHostReverseProxy(upstream)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "VariableStoreService") {
			_ = connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodeResourceExhausted, errors.New("cloud.rate_limited: try again in a minute")))
			return
		}
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(refusing.Close)
	ctx, span := remoteSpan(t)
	p, err := providerprocess.StartRemote(ctx, remoteProject(t), span, providerprocess.Questions{}, providerprocess.Remote{Name: "ocel", URL: refusing.URL, Tokens: &issuedTokens{tokens: []string{clitest.FakeCloudToken}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	store, err := p.VariableStore()
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.ListValues(t.Context(), &variablestorev1.ListValuesRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: "shop"})
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeProviderBusy || !coded.Retryable || connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("err = %v, want a retryable %s that keeps its Connect code", err, clierror.CodeProviderBusy)
	}
}

func TestAProjectWithTransformsIsRefusedBeforeOcelCloudIsCalled(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	cfg := remoteProject(t)
	cfg.Transforms = []string{"transforms/tag.ts"}

	_, err := startRemote(t, server, cfg, &issuedTokens{tokens: []string{clitest.FakeCloudToken}})
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported || !strings.Contains(err.Error(), "transforms") {
		t.Fatalf("err = %v, want transforms refused as cloud.unsupported", err)
	}
	if calls := server.Calls(); len(calls) != 0 {
		t.Fatalf("calls = %+v, want none", calls)
	}
}

func TestARemoteProviderStreamsAnOperationToItsResult(t *testing.T) {
	server := clitest.ServeRemoteProvider(t, fake.NewForProject(fake.Options{}))
	p, err := startRemote(t, server, remoteProject(t), &issuedTokens{tokens: []string{clitest.FakeCloudToken}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := remoteSpan(t)
	result, err := providerprocess.Stream(ctx, p, "RemoveStalePromotions", &contractv1.RemoveStalePromotionsRequest{Slug: "shop", Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}}, contractv1connect.ProviderServiceClient.RemoveStalePromotions)
	if err != nil || !result.GetSuccess() {
		t.Fatalf("result = %v, err = %v, want the stream to end in success", result, err)
	}
}

func TestARemoteProviderServedOverTLSIsReachedOverHTTP2ForCallsAndStreams(t *testing.T) {
	server, client := clitest.ServeRemoteProviderOverHTTP2(t, fake.NewForProject(fake.Options{}))
	ctx, span := remoteSpan(t)
	p, err := providerprocess.StartRemote(ctx, remoteProject(t), span, providerprocess.Questions{}, providerprocess.Remote{Name: "ocel", URL: server.URL, Tokens: &issuedTokens{tokens: []string{clitest.FakeCloudToken}}, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err := providerprocess.Stream(ctx, p, "RemoveStalePromotions", &contractv1.RemoveStalePromotionsRequest{Slug: "shop", Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}}, contractv1connect.ProviderServiceClient.RemoveStalePromotions); err != nil {
		t.Fatal(err)
	}
	if majors := server.ProtocolMajors(); len(majors) != 2 || majors[0] != 2 || majors[1] != 2 {
		t.Fatalf("protocols = %v, want Configure and the stream over HTTP/2", majors)
	}
}
