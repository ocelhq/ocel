package consolecredential_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/consolecredential"
	"github.com/ocelhq/ocel/cli/internal/githubactions/githubactionstest"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const projectID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"

type fakeTargets struct {
	consolev1connect.UnimplementedTargetServiceHandler

	lifetime    time.Duration
	refusal     *connect.Error
	refuseAfter int

	mu       sync.Mutex
	requests []*consolev1.IssueCredentialRequest
	bearers  []string
}

func (f *fakeTargets) IssueCredential(_ context.Context, req *consolev1.IssueCredentialRequest) (*consolev1.IssueCredentialResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.refusal != nil {
		return nil, f.refusal
	}
	if f.refuseAfter > 0 && len(f.requests) > f.refuseAfter {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this pipeline is no longer trusted"))
	}
	return &consolev1.IssueCredentialResponse{
		Token:       fmt.Sprintf("console-token-%d", len(f.requests)),
		ExpiresAt:   timestamppb.New(time.Now().Add(f.lifetime)),
		TargetId:    "0199c3a2-0000-7000-8000-000000000001",
		Account:     "123456789012",
		Region:      "eu-west-1",
		SessionName: "ocel-production-1",
		Identity:    &consolev1.TierIdentity{IdentityProvider: "issuer-provider", Principal: "deploy-production"},
	}, nil
}

func (f *fakeTargets) issued() []*consolev1.IssueCredentialRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func serveTargets(t *testing.T, targets *fakeTargets) string {
	t.Helper()
	path, handler := consolev1connect.NewTargetServiceHandler(targets)
	mux := http.NewServeMux()
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets.mu.Lock()
		targets.bearers = append(targets.bearers, r.Header.Get("Authorization"))
		targets.mu.Unlock()
		handler.ServeHTTP(w, r)
	})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func pipeline(t *testing.T, targets *fakeTargets, tokens *githubactionstest.IDTokens) consolecredential.Console {
	t.Helper()
	env := githubactionstest.Env(githubactionstest.ServeIDTokens(t, tokens))
	env[console.URLEnvVar] = serveTargets(t, targets)
	env[console.ProjectIDEnvVar] = projectID
	read, err := jobcontext.Read(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	return consolecredential.Console{Context: read, Version: "1.2.3", RefreshFloor: 10 * time.Millisecond}
}

func production() consolecredential.Request {
	return consolecredential.Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Vendor: "aws"}
}

func TestACredentialIsIssuedForTheProjectTierWithAFreshIDTokenAndKeptInAPrivateFile(t *testing.T) {
	targets := &fakeTargets{lifetime: time.Hour}
	tokens := &githubactionstest.IDTokens{}
	credential, err := pipeline(t, targets, tokens).Issue(context.Background(), production(), nil)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}
	defer credential.Close()

	sent := targets.issued()
	if len(sent) != 1 || sent[0].GetProjectId() != projectID || sent[0].GetTier() != environmentv1.Tier_TIER_PRODUCTION ||
		sent[0].GetVendor() != "aws" || sent[0].GetCliVersion() != "1.2.3" {
		t.Errorf("IssueCredential got %v, want the project, tier, vendor and CLI version", sent)
	}
	if targets.bearers[0] != "Bearer id-token-1" {
		t.Errorf("Authorization = %q, want the workflow's ID token", targets.bearers[0])
	}
	if audiences := tokens.Audiences(); len(audiences) != 1 || !strings.HasPrefix(audiences[0], "http://127.0.0.1:") {
		t.Errorf("ID token audiences = %v, want the console's origin", audiences)
	}

	identity := credential.WebIdentity()
	if identity.GetIdentityProvider() != "issuer-provider" || identity.GetPrincipal() != "deploy-production" || identity.GetSessionName() != "ocel-production-1" {
		t.Errorf("WebIdentity() = %v, want the identity and session the console named", identity)
	}
	if credential.Account() != "123456789012" || credential.Region() != "eu-west-1" {
		t.Errorf("Account(), Region() = %q, %q, want the target's", credential.Account(), credential.Region())
	}
	data, err := os.ReadFile(identity.GetTokenFile())
	if err != nil || string(data) != "console-token-1" {
		t.Fatalf("the token file holds %q, %v, want the issued token", data, err)
	}
	for path, want := range map[string]os.FileMode{identity.GetTokenFile(): 0o600, filepath.Dir(identity.GetTokenFile()): 0o700} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}

func TestACredentialIsIssuedAgainBeforeItExpiresUntilItIsClosedAndThenRemoved(t *testing.T) {
	targets := &fakeTargets{lifetime: 300 * time.Millisecond}
	tokens := &githubactionstest.IDTokens{}
	credential, err := pipeline(t, targets, tokens).Issue(context.Background(), production(), nil)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}
	tokenFile := credential.WebIdentity().GetTokenFile()

	deadline := time.Now().Add(5 * time.Second)
	for len(targets.issued()) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil || string(data) == "console-token-1" {
		t.Errorf("the token file holds %q, %v after %d issues, want a token issued after the first", data, err, len(targets.issued()))
	}
	if audiences := tokens.Audiences(); len(audiences) < 3 {
		t.Errorf("%d ID tokens requested for %d issues, want a fresh one each time", len(audiences), len(targets.issued()))
	}

	credential.Close()
	issued := len(targets.issued())
	if _, err := os.Stat(filepath.Dir(tokenFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the credential's directory is still there after Close: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := len(targets.issued()); got != issued {
		t.Errorf("%d credentials issued after Close, want none", got-issued)
	}
}

func TestAProjectTierBoundToNoTargetIsSaidAsSuch(t *testing.T) {
	targets := &fakeTargets{refusal: connect.NewError(connect.CodeNotFound, errors.New("no target is bound to this project's production tier"))}

	_, err := pipeline(t, targets, &githubactionstest.IDTokens{}).Issue(context.Background(), production(), nil)

	if !errors.Is(err, consolecredential.ErrNoTarget) {
		t.Errorf("Issue() = %v, want ErrNoTarget", err)
	}
}

func TestACredentialTheConsoleRefusesStopsWithTheConsolesReason(t *testing.T) {
	targets := &fakeTargets{refusal: connect.NewError(connect.CodeFailedPrecondition, errors.New("the production tier names no deploy identity; run ocel bootstrap production"))}

	_, err := pipeline(t, targets, &githubactionstest.IDTokens{}).Issue(context.Background(), production(), nil)

	if err == nil || !strings.Contains(err.Error(), "the production tier names no deploy identity; run ocel bootstrap production") || strings.Contains(err.Error(), "failed_precondition") {
		t.Errorf("Issue() = %v, want the console's own reason", err)
	}
}

func TestOnlyARunWithAJobContextAsksForACredential(t *testing.T) {
	if (consolecredential.Console{}).IsRequested() {
		t.Error("IsRequested() = true with no job context")
	}
	if !(consolecredential.Console{Context: &jobcontext.Context{}}).IsRequested() {
		t.Error("IsRequested() = false with a job context")
	}
}

func TestAJobIsIssuedACredentialWithItsJobTokenAsTheBearer(t *testing.T) {
	targets := &fakeTargets{lifetime: time.Hour}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("job-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := consolecredential.Console{Version: "1.2.3", Context: &jobcontext.Context{
		ConsoleURL: serveTargets(t, targets), ProjectID: projectID, Credential: jobcontext.JobToken{File: tokenFile},
		Job: &jobcontext.Job{ID: "job_1"},
	}}
	credential, err := job.Issue(context.Background(), production(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Close()
	if sent := targets.issued(); len(sent) != 1 || sent[0].GetProjectId() != projectID || sent[0].GetVendor() != "aws" {
		t.Errorf("IssueCredential got %v, want the job's project and the vendor", sent)
	}
	if targets.bearers[0] != "Bearer job-token" {
		t.Errorf("Authorization = %q, want the job token", targets.bearers[0])
	}
}

func TestACredentialThatHasAlreadyExpiredIsRefused(t *testing.T) {
	targets := &fakeTargets{lifetime: -time.Minute}

	_, err := pipeline(t, targets, &githubactionstest.IDTokens{}).Issue(context.Background(), production(), nil)

	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("Issue() = %v, want a refusal of a credential that has already expired", err)
	}
}

func TestARenewalTheConsoleRefusesForGoodStopsRenewingAndIsSaid(t *testing.T) {
	targets := &fakeTargets{lifetime: 100 * time.Millisecond, refuseAfter: 1}
	var mu sync.Mutex
	var warned []string
	credential, err := pipeline(t, targets, &githubactionstest.IDTokens{}).Issue(context.Background(), production(), func(line string) {
		mu.Lock()
		defer mu.Unlock()
		warned = append(warned, line)
	})
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	closeErr := credential.Close()

	if got := len(targets.issued()); got != 2 {
		t.Errorf("the console was asked %d times, want the first issue and one refused renewal and no more", got)
	}
	if closeErr == nil || !strings.Contains(closeErr.Error(), "no longer trusted") {
		t.Errorf("Close() = %v, want the refused renewal", closeErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(warned) != 1 || !strings.Contains(warned[0], "no longer trusted") {
		t.Errorf("warned %q, want one line naming the refused renewal", warned)
	}
}

func TestARenewalWaitsAtLeastTheFloorHoweverShortTheCredentialLives(t *testing.T) {
	targets := &fakeTargets{lifetime: time.Millisecond * 50}
	console := pipeline(t, targets, &githubactionstest.IDTokens{})
	console.RefreshFloor = 400 * time.Millisecond
	targets.lifetime = time.Second
	credential, err := console.Issue(context.Background(), production(), nil)
	if err != nil {
		t.Fatalf("Issue() = %v", err)
	}
	defer credential.Close()

	time.Sleep(200 * time.Millisecond)

	if got := len(targets.issued()); got != 1 {
		t.Errorf("the console was asked %d times within the floor, want once", got)
	}
}
