package console

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func TestCloudTokensReuseATokenOnlyWhileItHasMoreThanAMinuteLeft(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var scopes []string
	service := &consoleService{}
	service.issue = func(req *consolev1.IssueCloudTokenRequest) (*consolev1.IssueCloudTokenResponse, error) {
		scopes = req.GetScopes()
		return &consolev1.IssueCloudTokenResponse{
			Token:     fmt.Sprintf("cloud-%d", service.issued.Load()),
			ExpiresAt: timestamppb.New(now.Add(5 * time.Minute)),
		}, nil
	}
	tokens := NewCloudTokens(New(serveConsole(t, service)), "session", "jobs:write")
	tokens.now = func() time.Time { return now }

	first, err := tokens.Ensure(context.Background())
	if err != nil || first != "cloud-1" {
		t.Fatalf("Ensure = %q, %v, want the first token", first, err)
	}
	if bearer := service.authorized.Load(); bearer != "Bearer session" {
		t.Errorf("Authorization = %q, want the console session", bearer)
	}
	if !slices.Equal(scopes, []string{"jobs:write"}) {
		t.Errorf("scopes = %v, want [jobs:write]", scopes)
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
