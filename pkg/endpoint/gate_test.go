package endpoint_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	"github.com/ocelhq/ocel/pkg/proto/endpoint/v1/endpointv1connect"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/scope"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

const (
	endpointID     = "0199b5c4-0000-7000-8000-00000000cafe"
	organizationID = "org-shop"
	targetID       = "0199b5c4-0000-7000-8000-00000000beef"
	credentialAud  = "ocel-target:" + targetID
)

type console struct {
	origin string
	signer jwk.Key
	keys   *httptest.Server
}

func signingKeys(t *testing.T) (jwk.Key, jwk.Set) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jwk.Import(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Set(jwk.KeyIDKey, "key-1"); err != nil {
		t.Fatal(err)
	}
	published, err := jwk.Import(public)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{jwk.KeyIDKey: "key-1", jwk.AlgorithmKey: jwa.EdDSA()} {
		if err := published.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(published); err != nil {
		t.Fatal(err)
	}
	return signer, set
}

func consoleServing(t *testing.T) *console {
	t.Helper()
	signer, set := signingKeys(t)
	mux := http.NewServeMux()
	serveSet := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}
	mux.HandleFunc("GET /api/auth/jwks", serveSet)
	mux.HandleFunc("GET /oidc/jwks", serveSet)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &console{origin: server.URL, signer: signer, keys: server}
}

type minted struct {
	issuer       string
	audience     string
	subject      string
	claims       map[string]any
	neverExpires bool
}

func (c *console) token(t *testing.T, m minted) string {
	t.Helper()
	issuer := m.issuer
	if issuer == "" {
		issuer = c.origin
	}
	builder := jwt.NewBuilder().
		Issuer(issuer).
		Audience([]string{m.audience}).
		Subject(m.subject).
		JwtID("jti-1").
		IssuedAt(time.Now())
	for name, value := range m.claims {
		builder = builder.Claim(name, value)
	}
	if !m.neverExpires {
		builder = builder.Expiration(time.Now().Add(time.Minute))
	}
	token, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.EdDSA(), c.signer))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func (c *console) user(t *testing.T, scopes ...string) string {
	return c.token(t, minted{audience: endpointID, subject: "org:" + organizationID, claims: map[string]any{"act": "user-1", "scope": scopes}})
}

func (c *console) job(t *testing.T, runner string) string {
	return c.token(t, minted{
		audience: endpoint.JobAudience,
		subject:  "org:" + organizationID + ":tier:production:project:proj-1",
		claims:   map[string]any{"job_id": "job-1", "deployment_id": "0123456789abcdef0123456789abcdef", "runner": runner, "slug": "shop"},
	})
}

func (c *console) credential(t *testing.T, target string) string {
	return c.token(t, minted{
		issuer:   c.origin + "/oidc",
		audience: "ocel-target:" + target,
		subject:  "org:" + organizationID + ":target:" + target + ":tier:production:project:proj-1",
		claims:   map[string]any{"org": organizationID, "target": target, "tier": "production", "project": "proj-1", "purpose": "deploy"},
	})
}

type runner struct {
	runnerv1connect.UnimplementedRunnerServiceHandler
	callers chan endpoint.Caller
}

func (r runner) StartJob(ctx context.Context, _ *runnerv1.StartJobRequest) (*runnerv1.StartJobResponse, error) {
	caller, _ := endpoint.CallerOf(ctx)
	r.callers <- caller
	return &runnerv1.StartJobResponse{}, nil
}

type certificates struct {
	endpointv1connect.UnimplementedSshCertificateServiceHandler
	callers chan endpoint.Caller
}

func (c certificates) IssueUserCertificate(ctx context.Context, _ *endpointv1.IssueUserCertificateRequest) (*endpointv1.IssueUserCertificateResponse, error) {
	caller, _ := endpoint.CallerOf(ctx)
	c.callers <- caller
	return &endpointv1.IssueUserCertificateResponse{}, nil
}

type served struct {
	url          string
	client       *http.Client
	jobs         chan endpoint.Caller
	certificates chan endpoint.Caller
}

func serve(t *testing.T, at *console, allowed []string) *served {
	t.Helper()
	jobs, certified := make(chan endpoint.Caller, 1), make(chan endpoint.Caller, 1)
	runnerPath, runnerHandler := runnerv1connect.NewRunnerServiceHandler(runner{callers: jobs})
	certificatePath, certificateHandler := endpointv1connect.NewSshCertificateServiceHandler(certificates{callers: certified})
	s := serveMounts(t, at, allowed,
		endpoint.VariablesMount(variablestoreserver.FixedBackend{KeyValues: fake.NewKeyValues(), Cipher: fake.NewCipher()}),
		endpoint.Mount{Path: runnerPath, Handler: runnerHandler},
		endpoint.Mount{Path: certificatePath, Handler: certificateHandler},
	)
	s.jobs, s.certificates = jobs, certified
	return s
}

func serveMounts(t *testing.T, at *console, allowed []string, mounts ...endpoint.Mount) *served {
	t.Helper()
	handler, err := endpoint.New(endpoint.Spec{
		Config: endpoint.Config{
			Console:        at.origin,
			EndpointID:     endpointID,
			OrganizationID: organizationID,
			AllowedScopes:  allowed,
			Credentials:    &endpoint.CredentialTrust{Issuer: at.origin + "/oidc", Audience: credentialAud, TargetID: targetID},
		},
		Mounts: mounts,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &served{url: server.URL, client: server.Client()}
}

func bearing(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

func (s *served) listValues(t *testing.T, token string) error {
	t.Helper()
	_, err := variablestorev1connect.NewVariableStoreServiceClient(s.client, s.url, bearing(token)).
		ListValues(t.Context(), &variablestorev1.ListValuesRequest{Tier: 1, Slug: "shop"})
	return err
}

func (s *served) revealValues(t *testing.T, token string) error {
	t.Helper()
	_, err := variablestorev1connect.NewVariableStoreServiceClient(s.client, s.url, bearing(token)).
		RevealValues(t.Context(), &variablestorev1.RevealValuesRequest{Tier: 1, Slug: "shop"})
	return err
}

func (s *served) revealProtectionPassword(t *testing.T, token string) error {
	t.Helper()
	_, err := contractv1connect.NewProviderServiceClient(s.client, s.url, bearing(token)).
		RevealProtectionPassword(t.Context(), &contractv1.RevealProtectionPasswordRequest{Tier: 1, Slug: "shop"})
	return err
}

func (s *served) startJob(t *testing.T, token string) error {
	t.Helper()
	_, err := runnerv1connect.NewRunnerServiceClient(s.client, s.url, bearing(token)).
		StartJob(t.Context(), &runnerv1.StartJobRequest{})
	return err
}

func (s *served) issueCertificate(t *testing.T, token string) error {
	t.Helper()
	_, err := endpointv1connect.NewSshCertificateServiceClient(s.client, s.url, bearing(token)).
		IssueUserCertificate(t.Context(), &endpointv1.IssueUserCertificateRequest{})
	return err
}

func wantCode(t *testing.T, err error, want connect.Code, why string) {
	t.Helper()
	if connect.CodeOf(err) != want {
		t.Errorf("code = %v (err = %v), want %v: %s", connect.CodeOf(err), err, want, why)
	}
}

func TestAUserTokenHoldingAnAllowedScopeIsAdmitted(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, []string{"variables.read"})

	if err := s.listValues(t, at.user(t, "variables.read")); err != nil {
		t.Fatalf("ListValues with variables.read = %v, want it served", err)
	}
}

func TestAUserTokenIsRefusedWhatItsScopeOrTheEndpointDoesNotAllow(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, []string{"variables.read"})

	wantCode(t, s.listValues(t, at.user(t, "apps.read")), connect.CodePermissionDenied, "the token holds no variables.read")
	wantCode(t, s.revealValues(t, at.user(t, "variables.read", "variables.reveal")), connect.CodePermissionDenied,
		"the endpoint allows no variables.reveal, whatever the token holds")
}

func TestRevealingAProtectionPasswordNeedsTheScopeThatChangesIt(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	providerPath, providerHandler := contractv1connect.NewProviderServiceHandler(contractv1connect.UnimplementedProviderServiceHandler{})
	s := serveMounts(t, at, scope.ClaimsOf(scope.ListUserScopes()), endpoint.Mount{Path: providerPath, Handler: providerHandler})

	wantCode(t, s.revealProtectionPassword(t, at.user(t, "apps.read")), connect.CodePermissionDenied,
		"apps.read is a member's read token, and it reads no password")
	wantCode(t, s.revealProtectionPassword(t, at.user(t, "apps.write")), connect.CodeUnimplemented,
		"whoever may change the protection may read it back, so the gate lets apps.write through to the handler")
}

func TestATokenForAnotherOrganizationIsRefused(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, []string{"variables.read"})

	token := at.token(t, minted{audience: endpointID, subject: "org:someone-else", claims: map[string]any{"scope": []string{"variables.read"}}})
	wantCode(t, s.listValues(t, token), connect.CodePermissionDenied, "a connector serves its own organization alone")
}

func TestATokenThatIsUnsignedForThisEndpointOrNeverExpiresIsRefused(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, []string{"variables.read"})
	stranger := consoleServing(t)

	for why, token := range map[string]string{
		"no token":            "",
		"another audience":    at.token(t, minted{audience: "another-endpoint", subject: "org:" + organizationID, claims: map[string]any{"scope": []string{"variables.read"}}}),
		"never expires":       at.token(t, minted{audience: endpointID, subject: "org:" + organizationID, claims: map[string]any{"scope": []string{"variables.read"}}, neverExpires: true}),
		"another issuer":      stranger.user(t, "variables.read"),
		"another console key": stranger.token(t, minted{issuer: at.origin, audience: endpointID, subject: "org:" + organizationID, claims: map[string]any{"scope": []string{"variables.read"}}}),
	} {
		wantCode(t, s.listValues(t, token), connect.CodeUnauthenticated, why)
	}
}

func TestACallWhileTheConsolesKeysCannotBeReachedIsUnavailable(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, []string{"variables.read"})
	token := at.user(t, "variables.read")
	at.keys.Close()

	wantCode(t, s.listValues(t, token), connect.CodeUnavailable, "the caller may retry once the keys are reachable")
}

func TestAJobTokenStartsAJobOnTheRunnerItNamesAndNothingElse(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, scope.ClaimsOf(scope.ListUserScopes()))

	if err := s.startJob(t, at.job(t, endpointID)); connect.CodeOf(err) == connect.CodePermissionDenied || connect.CodeOf(err) == connect.CodeUnauthenticated {
		t.Fatalf("StartJob with this runner's job token = %v, want it admitted", err)
	}
	caller := <-s.jobs
	want := endpoint.Job{ID: "job-1", ProjectID: "proj-1", DeploymentID: "0123456789abcdef0123456789abcdef", Runner: endpointID, Slug: "shop", TokenID: "jti-1", Tier: environment.TierProduction}
	got, _ := caller.Job()
	got.Expires = time.Time{}
	if got != want || caller.OrganizationID != organizationID {
		t.Errorf("the job's caller = %+v in %s, want %+v in %s", got, caller.OrganizationID, want, organizationID)
	}

	wantCode(t, s.startJob(t, at.job(t, "another-runner")), connect.CodePermissionDenied, "a job token works on the runner it names alone")
	wantCode(t, s.listValues(t, at.job(t, endpointID)), connect.CodePermissionDenied, "a job token holds the job scope alone")
	wantCode(t, s.startJob(t, at.user(t, scope.ClaimsOf(scope.ListUserScopes())...)), connect.CodePermissionDenied, "no person's token starts a job")
}

func TestATargetCredentialIsAdmittedForCertificatesOfItsTargetAlone(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, scope.ClaimsOf(scope.ListUserScopes()))

	if err := s.issueCertificate(t, at.credential(t, targetID)); err != nil {
		t.Fatalf("IssueUserCertificate with this target's credential = %v, want it admitted", err)
	}
	caller := <-s.certificates
	if credential, ok := caller.Credential(); !ok || credential.TargetID != targetID || credential.Tier != environment.TierProduction || credential.Purpose != "deploy" {
		t.Errorf("the certificate's caller = %+v, want the credential for %s", credential, targetID)
	}

	wantCode(t, s.issueCertificate(t, at.credential(t, "0199b5c4-0000-7000-8000-000000000000")), connect.CodeUnauthenticated, "a credential for another target is for another audience")
	wantCode(t, s.listValues(t, at.credential(t, targetID)), connect.CodePermissionDenied, "a credential holds the target.credential scope alone")
	wantCode(t, s.issueCertificate(t, at.user(t, scope.ClaimsOf(scope.ListUserScopes())...)), connect.CodePermissionDenied, "no person's token signs a certificate")
	wantCode(t, s.issueCertificate(t, at.job(t, endpointID)), connect.CodePermissionDenied, "a job token signs no certificate")
}

func TestAPathNoMountServesIsUnimplemented(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, []string{"variables.read"})

	_, err := connect.NewClient[variablestorev1.ListValuesRequest, variablestorev1.ListValuesResponse](
		s.client, s.url+"/provider.contract.v1.ProviderService/ListProjects", bearing(at.user(t, "apps.read")),
	).CallUnary(t.Context(), connect.NewRequest(&variablestorev1.ListValuesRequest{}))
	wantCode(t, err, connect.CodeUnimplemented, "nothing is mounted there")
}

func TestAnUndeclaredMethodUnderAMountedServiceIsRefused(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, scope.ClaimsOf(scope.ListUserScopes()))

	_, err := connect.NewClient[variablestorev1.ListValuesRequest, variablestorev1.ListValuesResponse](
		s.client, s.url+"/provider.variablestore.v1.VariableStoreService/Sneak", bearing(at.user(t, scope.ClaimsOf(scope.ListUserScopes())...)),
	).CallUnary(t.Context(), connect.NewRequest(&variablestorev1.ListValuesRequest{}))
	wantCode(t, err, connect.CodePermissionDenied, "a method no option names a scope for is refused under every scope")
}

func TestTheServicesAnEndpointReportsAreTheOnesItMounts(t *testing.T) {
	t.Parallel()
	runnerPath, runnerHandler := runnerv1connect.NewRunnerServiceHandler(runnerv1connect.UnimplementedRunnerServiceHandler{})
	spec := endpoint.Spec{Mounts: []endpoint.Mount{
		{Path: runnerPath, Handler: runnerHandler},
		endpoint.VariablesMount(variablestoreserver.FixedBackend{}),
		{Path: "/blobs/", Handler: http.NotFoundHandler(), SelfAuthenticated: true},
	}}
	want := []string{variablestorev1connect.VariableStoreServiceName, runnerv1connect.RunnerServiceName}
	if got := spec.Services(); !slices.Equal(got, want) {
		t.Errorf("Services() = %v, want %v", got, want)
	}
}

func TestAMountAtAPathNoServiceNamesMustAuthenticateItself(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	config := endpoint.Config{Console: at.origin, EndpointID: endpointID, OrganizationID: organizationID}

	if _, err := endpoint.New(endpoint.Spec{Config: config, Mounts: []endpoint.Mount{{Path: "/blobs/", Handler: http.NotFoundHandler()}}}); err == nil {
		t.Error("New() mounting /blobs/ behind the gate = nil, want it refused: no method option names a scope for it")
	}

	reached := false
	handler, err := endpoint.New(endpoint.Spec{Config: config, Mounts: []endpoint.Mount{{
		Path:              "/blobs/",
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusNoContent) }),
		SelfAuthenticated: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	response, err := server.Client().Get(server.URL + "/blobs/abc")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !reached || response.StatusCode != http.StatusNoContent {
		t.Errorf("a self-authenticated mount answered %d (reached = %v), want its own handler to answer", response.StatusCode, reached)
	}
}

func (c *console) previewJob(t *testing.T, tier, preview string) string {
	return c.token(t, minted{
		audience: endpoint.JobAudience,
		subject:  "org:" + organizationID + ":tier:" + tier + ":project:proj-1",
		claims:   map[string]any{"job_id": "job-2", "runner": endpointID, "slug": "shop", "preview": preview},
	})
}

func TestAPreviewJobTokenNamesThePreviewItActsOn(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	s := serve(t, at, nil)

	if err := s.startJob(t, at.previewJob(t, "preview", "pr-7")); err != nil {
		t.Fatalf("StartJob with a preview job token = %v, want it admitted", err)
	}
	if job, _ := (<-s.jobs).Job(); job.Preview != "pr-7" || job.Tier != environment.TierPreview {
		t.Errorf("the job's caller = %+v, want preview pr-7 in the preview tier", job)
	}
	wantCode(t, s.startJob(t, at.previewJob(t, "production", "pr-7")), connect.CodePermissionDenied, "an authentic token naming a preview in production names what it may not")
}

func TestAnEndpointServesTheOrganizationItNamesUnlessItSaysItServesMany(t *testing.T) {
	t.Parallel()
	at := consoleServing(t)
	mounts := []endpoint.Mount{endpoint.VariablesMount(variablestoreserver.FixedBackend{KeyValues: fake.NewKeyValues(), Cipher: fake.NewCipher()})}

	if _, err := endpoint.New(endpoint.Spec{Config: endpoint.Config{Console: at.origin, EndpointID: endpointID}, Mounts: mounts}); err == nil {
		t.Error("New() naming no organization = nil, want it refused unless it serves many")
	}
	if _, err := endpoint.New(endpoint.Spec{Config: endpoint.Config{Console: at.origin, OrganizationID: organizationID}, Mounts: mounts}); err == nil {
		t.Error("New() naming no endpoint id = nil, want it refused")
	}
	handler, err := endpoint.New(endpoint.Spec{
		Config:                  endpoint.Config{Console: at.origin, EndpointID: endpointID, AllowedScopes: []string{"variables.read"}},
		ServesManyOrganizations: true,
		Mounts:                  mounts,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	s := &served{url: server.URL, client: server.Client()}
	token := at.token(t, minted{audience: endpointID, subject: "org:someone-else", claims: map[string]any{"scope": []string{"variables.read"}}})
	if err := s.listValues(t, token); err != nil {
		t.Errorf("ListValues for another organization at an endpoint serving many = %v, want it served", err)
	}
}
