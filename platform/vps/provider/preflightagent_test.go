package vps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func agentSurveyed(t *testing.T, content []byte) answer {
	t.Helper()
	sum := sha256.Sum256(content)
	return answer{stdout: host.KindFile + "\t" + host.LiveBinary + "\t755\troot\t" + hex.EncodeToString(sum[:]) + "\n"}
}

func currentAgent(t *testing.T) []byte {
	t.Helper()
	for _, item := range host.LiveItems(host.ArchAMD64) {
		if item.Name == host.LiveBinary {
			return item.Content
		}
	}
	t.Fatal("no live item installs the agent")
	return nil
}

func preflightingResources(t *testing.T, machine *scripted, declared ...provider.Resource) error {
	t.Helper()
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	p.Reaching(reachedFromOutside)
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return p.PreflightDeploy(context.Background(), provider.DeployPreflight{
		Deploy: provider.DeploySpec{
			Slug: "shop",
			Tier: environment.TierProduction,
			Apps: []provider.AppEntry{{App: "web", Stack: stack, Image: deployedRef}},
		},
		Resources: declared,
	})
}

var aDeclaredTask = provider.Resource{Name: "task--receipt", Declared: "receipt", Type: provider.BindingTask}

func TestADeployDeclaringATaskIsRefusedOnABoxWhoseAgentPredatesThisBuild(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{
		"uname -m":      {stdout: "x86_64\n"},
		host.LiveBinary: agentSurveyed(t, []byte("an agent an older ocel installed")),
	})
	err := preflightingResources(t, machine, aDeclaredTask)
	if err == nil {
		t.Fatal("PreflightDeploy() let a task onto a box whose agent serves no queue, and every trigger would fail at runtime")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("PreflightDeploy() = %q, want it to name `ocel bootstrap` as the remedy", err)
	}
}

func TestADeployDeclaringATaskIsLetOntoABoxRunningThisBuildsAgent(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{
		"uname -m":      {stdout: "x86_64\n"},
		host.LiveBinary: agentSurveyed(t, currentAgent(t)),
	})
	if err := preflightingResources(t, machine, aDeclaredTask); err != nil {
		t.Fatalf("PreflightDeploy() = %v, want a box running this build's agent let through", err)
	}
}

func TestADeployDeclaringNoTopicOrTaskNeverAsksAboutTheAgent(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{"uname -m": {stdout: "x86_64\n"}})
	if err := preflightingResources(t, machine); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}
	for _, command := range machine.ran {
		if strings.Contains(command, host.LiveBinary) {
			t.Errorf("a deploy with no topic or task asked about the agent: %q", command)
		}
	}
}
