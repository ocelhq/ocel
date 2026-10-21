package env

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
)

func TestEnvReadsAndWritesTheVariableStoreOfOcelCloudThroughTheProviderItServes(t *testing.T) {
	project := clitest.SetUpCloudProject(t)
	cloudDependencies := func(stream *bytes.Buffer) Dependencies {
		project.Cloud.TokenScopes = nil
		dependencies := newStreamedDependencies(stream)
		project.LogIn(&dependencies.Invocation)
		return dependencies
	}
	seedValue(t, project.FakeProject, environment.TierProduction, clitest.FixtureSlug, cellAt("LOG_LEVEL"), "debug")

	var stdout, stderr bytes.Buffer
	if err := runEnvGet(context.Background(), cloudDependencies(&stderr), project.Root, "LOG_LEVEL", envOptions{reveal: true}, &stdout, &stderr); err != nil {
		t.Fatalf("get err = %v; stderr=%s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "debug" {
		t.Fatalf("get = %q, want the value Ocel Cloud's store holds", stdout.String())
	}
	requireTokenScopes(t, project.Cloud, "cloud.apps.read", "cloud.variables.reveal")

	stdout.Reset()
	if err := runEnvGet(context.Background(), cloudDependencies(&stderr), project.Root, "LOG_LEVEL", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("get err = %v; stderr=%s", err, stderr.String())
	}
	requireTokenScopes(t, project.Cloud, "cloud.apps.read")

	stdout.Reset()
	if err := runEnvHistory(context.Background(), cloudDependencies(&stderr), project.Root, "LOG_LEVEL", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("history err = %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1") {
		t.Fatalf("history = %q, want the version Ocel Cloud's store holds", stdout.String())
	}
	requireTokenScopes(t, project.Cloud, "cloud.apps.read")

	stdout.Reset()
	if err := runEnvList(context.Background(), cloudDependencies(&stderr), project.Root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("ls err = %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String()+stderr.String(), "LOG_LEVEL") {
		t.Fatalf("ls = %q, want LOG_LEVEL listed", stdout.String())
	}
	requireTokenScopes(t, project.Cloud, "cloud.apps.read")

	stdout.Reset()
	if err := runEnvSet(context.Background(), cloudDependencies(&stderr), project.Root, "LOG_LEVEL", "info", envOptions{}, strings.NewReader(""), &stdout, &stderr); err != nil && !strings.Contains(err.Error(), "no app in this project declares LOG_LEVEL") {
		t.Fatalf("set err = %v; stderr=%s", err, stderr.String())
	}
	requireTokenScopes(t, project.Cloud, "cloud.apps.read", "cloud.variables.write")

	stdout.Reset()
	if err := runEnvRemove(context.Background(), cloudDependencies(&stderr), project.Root, "LOG_LEVEL", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("rm err = %v; stderr=%s", err, stderr.String())
	}
	requireTokenScopes(t, project.Cloud, "cloud.apps.read", "cloud.variables.write")
}

func requireTokenScopes(t *testing.T, cloud *clitest.FakeOcelCloud, want ...string) {
	t.Helper()
	if len(cloud.TokenScopes) == 0 || slices.ContainsFunc(cloud.TokenScopes, func(got []string) bool { return !slices.Equal(got, want) }) {
		t.Fatalf("tokens issued with scopes %v, want each with exactly %v", cloud.TokenScopes, want)
	}
}
