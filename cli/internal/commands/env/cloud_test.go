package env

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
)

func TestEnvReadsAndWritesTheVariableStoreOfOcelCloudThroughTheProviderItServes(t *testing.T) {
	project := clitest.SetUpCloudProject(t)
	cloudDependencies := func(stream *bytes.Buffer) Dependencies {
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

	stdout.Reset()
	if err := runEnvHistory(context.Background(), cloudDependencies(&stderr), project.Root, "LOG_LEVEL", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("history err = %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1") {
		t.Fatalf("history = %q, want the version Ocel Cloud's store holds", stdout.String())
	}

	stdout.Reset()
	if err := runEnvList(context.Background(), cloudDependencies(&stderr), project.Root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("ls err = %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String()+stderr.String(), "LOG_LEVEL") {
		t.Fatalf("ls = %q, want LOG_LEVEL listed", stdout.String())
	}
}
