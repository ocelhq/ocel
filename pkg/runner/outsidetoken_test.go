package runner_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/runner"
)

func TestAJobRequestOutsideItsTokenIsRefusedPermission(t *testing.T) {
	preview := func(name string) *runnerv1.JobRequest {
		return &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, Preview: &runnerv1.PreviewTarget{Name: name}}
	}
	deploy := &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_DEPLOY}
	for name, tc := range map[string]struct {
		token   endpoint.Job
		request *runnerv1.JobRequest
		allowed bool
	}{
		"the token's preview":         {endpoint.Job{Tier: environment.TierPreview, Preview: "pr-1"}, preview("pr-1"), true},
		"another preview":             {endpoint.Job{Tier: environment.TierPreview, Preview: "pr-1"}, preview("pr-2"), false},
		"a preview with no preview":   {endpoint.Job{Tier: environment.TierPreview}, preview("pr-1"), false},
		"a preview on production":     {endpoint.Job{Tier: environment.TierProduction, Preview: "pr-1"}, preview("pr-1"), false},
		"a deploy on production":      {endpoint.Job{Tier: environment.TierProduction}, deploy, true},
		"a deploy on preview":         {endpoint.Job{Tier: environment.TierPreview}, deploy, false},
		"a deploy naming a preview":   {endpoint.Job{Tier: environment.TierProduction, Preview: "pr-1"}, deploy, false},
		"a build on the preview tier": {endpoint.Job{Tier: environment.TierPreview}, &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD}, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := runner.RefuseOutsideToken(tc.token, tc.request)
			if tc.allowed != (err == nil) || (err != nil && connect.CodeOf(err) != connect.CodePermissionDenied) {
				t.Fatalf("RefuseOutsideToken() = %v, want allowed %t (or permission denied)", err, tc.allowed)
			}
		})
	}
}
