package console

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func TestEndpointTokensReuseATokenOnlyWhileItHasMoreThanAMinuteLeft(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var scopes []scopev1.Scope
	var organization, endpoint string
	service := &consoleService{}
	service.issue = func(req *consolev1.IssueEndpointTokenRequest) (*consolev1.IssueEndpointTokenResponse, error) {
		scopes = req.GetScopes()
		organization = req.GetOrganizationId()
		endpoint = req.GetEndpointId()
		return &consolev1.IssueEndpointTokenResponse{
			Token:     fmt.Sprintf("cloud-%d", service.issued.Load()),
			ExpiresAt: timestamppb.New(now.Add(5 * time.Minute)),
		}, nil
	}
	tokens := NewEndpointTokens(New(serveConsole(t, service)), "session", "org_1", CloudEndpointID, scopev1.Scope_SCOPE_APPS_READ, scopev1.Scope_SCOPE_APPS_WRITE)
	tokens.now = func() time.Time { return now }

	first, err := tokens.Ensure(context.Background())
	if err != nil || first != "cloud-1" {
		t.Fatalf("Ensure = %q, %v, want the first token", first, err)
	}
	if bearer := service.authorized.Load(); bearer != "Bearer session" {
		t.Errorf("Authorization = %q, want the console session", bearer)
	}
	if !slices.Equal(scopes, []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ, scopev1.Scope_SCOPE_APPS_WRITE}) {
		t.Errorf("scopes = %v, want apps read and write", scopes)
	}
	if endpoint != CloudEndpointID {
		t.Errorf("endpoint = %q, want Ocel Cloud's", endpoint)
	}
	if organization != "org_1" {
		t.Errorf("organization = %q, want the organization the run picked", organization)
	}

	now = now.Add(3*time.Minute + 59*time.Second)
	if reused, err := tokens.Ensure(context.Background()); err != nil || reused != "cloud-1" {
		t.Errorf("Ensure with 61s left = %q, %v, want the held token", reused, err)
	}

	now = now.Add(time.Second)
	if fresh, err := tokens.Ensure(context.Background()); err != nil || fresh != "cloud-2" {
		t.Errorf("Ensure with 60s left = %q, %v, want a fresh token", fresh, err)
	}
	if service.issued.Load() != 2 {
		t.Errorf("issued %d tokens, want 2", service.issued.Load())
	}
}
