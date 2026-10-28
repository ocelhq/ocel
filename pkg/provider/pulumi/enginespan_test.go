package pulumi

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
)

func TestAnEngineSpanNamesTheStackWithASecretTheRunKnowsMasked(t *testing.T) {
	t.Parallel()
	setup := WorkspaceSpec{
		Stack:   "prod",
		Secrets: []string{"hunter2"},
		Project: workspace.Project{Name: "shop", Backend: &workspace.ProjectBackend{URL: "https://ops:hunter2@state.example.com/shop"}},
	}

	attrs := engineSpanAttributes(setup)
	for _, keyValue := range attrs {
		if strings.Contains(keyValue.Value.String(), "hunter2") {
			t.Fatalf("%s = %q carries a secret the run knows", keyValue.Key, keyValue.Value.String())
		}
	}
	if attrs[0].Value.AsString() != "prod" {
		t.Errorf("pulumi.stack = %q, want prod", attrs[0].Value.AsString())
	}
}
