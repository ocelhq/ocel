package deploy

import (
	"reflect"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestPreviewStacksFromNames_OneEntryPerPointerWithInferredLifecycle(t *testing.T) {
	got := previewStacksFromNames("shop", []string{
		// staging: persistent — owns a per-name infra stack plus an app stack.
		PreviewInfraStackName("shop", "staging"),
		PreviewAppDeployStackName("shop", "staging", "web", "b1"),
		// pr-1: ephemeral — several builds across apps, no infra stack, collapses to one.
		PreviewAppDeployStackName("shop", "pr-1", "web", "b2"),
		PreviewAppDeployStackName("shop", "pr-1", "web", "b3"),
		PreviewAppDeployStackName("shop", "pr-1", "api", "b4"),
		// Not previews of this project.
		InfraStackName("shop"),
		AppDeployStackName("shop", "web", "b9"),
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
		"shop-preview-feature_login_ab12", // retired "<projectID>-preview-<identity>"
		"other--preview-x--web--b1",       // another project's preview
		InfraStackName("shop"),            // production infra
		AppDeployStackName("shop", "web", "b1"),
	})
	if len(got) != 0 {
		t.Errorf("previewStacksFromNames matched retired/foreign/production stacks: %+v", got)
	}
}
