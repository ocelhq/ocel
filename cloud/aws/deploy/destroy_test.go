package deploy

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// A lock left by a killed run fails every later teardown of that stack the same
// way, and the CLI offers no unlock: the error is the only place the way out can
// be stated.
func TestLockRecoveryHint_NamesTheReleaseCommandForAStaleLockOnly(t *testing.T) {
	cfg := TeardownConfig{StackName: "shop--preview-pr-1--infra", BackendURL: "s3://state-bucket"}

	locked := errors.New("the stack is currently locked by 1 lock(s). Either wait for the other process(es) to end or delete the lock file with 'pulumi cancel'")
	hint := lockRecoveryHint(locked, cfg)
	for _, want := range []string{"pulumi cancel", cfg.StackName, cfg.BackendURL} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q does not mention %q", hint, want)
		}
	}

	if got := lockRecoveryHint(errors.New("resource still has dependencies"), cfg); got != "" {
		t.Errorf("hint on an unrelated failure = %q, want empty", got)
	}
	if got := lockRecoveryHint(nil, cfg); got != "" {
		t.Errorf("hint on no error = %q, want empty", got)
	}
}

func TestPreviewStacksFromNames_OneEntryPerPointerWithInferredLifecycle(t *testing.T) {
	got := previewStacksFromNames("shop", []string{
		// staging: persistent — owns a per-name infra stack plus an app stack.
		PreviewInfraStackName("shop", "staging"),
		PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1")),
		// pr-1: ephemeral — several builds across apps, no infra stack, collapses to one.
		PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b2")),
		PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b3")),
		PreviewAppDeployStackName("shop", "pr-1", "api", buildOnly("b4")),
		// Not previews of this project.
		InfraStackName("shop"),
		AppDeployStackName("shop", "web", buildOnly("b9")),
		"other--preview-x--web--b1",   // another project's preview
		"shop-preview-legacy",         // retired single-stack shape
		"shopfoo--preview-y--web--b1", // sibling whose id has ours as a prefix
	})

	want := []PreviewStack{
		{Identity: "pr-1", Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL},
		{Identity: "staging", Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("previewStacksFromNames = %+v, want %+v", got, want)
	}
}

func TestPreviewStacksFromNames_RetiredShapeAndForeignProjectsExcluded(t *testing.T) {
	got := previewStacksFromNames("shop", []string{
		"shop-preview-feature_login_ab12", // retired "<slug>-preview-<identity>"
		"other--preview-x--web--b1",       // another project's preview
		InfraStackName("shop"),            // production infra
		AppDeployStackName("shop", "web", buildOnly("b1")),
	})
	if len(got) != 0 {
		t.Errorf("previewStacksFromNames matched retired/foreign/production stacks: %+v", got)
	}
}
