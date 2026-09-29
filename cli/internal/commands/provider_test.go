package commands_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAnOpenedProviderIsAskedOnlyWhatTheCommandRequires(t *testing.T) {
	open := func(t *testing.T, project clitest.FakeProject, opts commands.OpenOptions) error {
		t.Helper()
		invocation := clitest.NewInvocation()
		cfg, err := invocation.LoadProject(context.Background(), project.Root)
		if err != nil {
			t.Fatalf("LoadProject: %v", err)
		}
		ctx, run, err := invocation.Events.Begin(context.Background(), "ocel test", cfg.Dir)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		defer run.End(&err)
		provider, read, err := invocation.OpenProvider(ctx, run.Phase(progressv1.Phase_PHASE_CHECK), cfg, opts)
		if err != nil {
			return err
		}
		defer provider.Close()
		if opts.Require == readiness.None && read.Response != nil {
			t.Errorf("OpenProvider asked the provider %v although nothing was required of it", read.Response)
		}
		if opts.Require != readiness.None && (read.Response == nil || read.Project == nil) {
			t.Error("OpenProvider returned no readiness or no resolved project for a command that required some")
		}
		return nil
	}

	t.Run("a provider asked for nothing opens without a preflight", func(t *testing.T) {
		if err := open(t, clitest.SetUpProject(t), commands.OpenOptions{}); err != nil {
			t.Fatalf("OpenProvider: %v", err)
		}
	})

	t.Run("a bootstrapped tier opens and says how ready it is", func(t *testing.T) {
		if err := open(t, clitest.SetUpProject(t), commands.OpenOptions{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: readiness.Infrastructure}); err != nil {
			t.Fatalf("OpenProvider: %v", err)
		}
	})

	t.Run("a tier with no infrastructure is refused naming the bootstrap that sets it up", func(t *testing.T) {
		err := open(t, clitest.SetUpProject(t), commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Infrastructure})
		if err == nil {
			t.Fatal("OpenProvider opened a provider for a command whose tier has no infrastructure")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap preview") {
			t.Errorf("refusal %q does not name `ocel bootstrap preview`", err)
		}
	})

	t.Run("credentials the provider refuses stop even a command that needs no infrastructure", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		project.Provider.Credentials().(*fake.Credentials).Deny("sign in again")
		err := open(t, project, commands.OpenOptions{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: readiness.Credentials})
		if err == nil || !strings.Contains(err.Error(), "could not authenticate") {
			t.Fatalf("OpenProvider err = %v, want the refused credentials named", err)
		}
	})
}

func TestWithProviderHandsTheWorkAnOpenProviderAndEndsTheRunWithItsError(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	invocation := clitest.NewInvocation()
	var rendered strings.Builder
	clitest.AttachTerminalSink(invocation, &rendered)
	cfg, err := invocation.LoadProject(context.Background(), fixture.Root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	refused := errors.New("the work refused")
	var named string
	err = invocation.WithProvider(context.Background(), cfg, "ocel test", commands.OpenOptions{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: readiness.Infrastructure}, func(_ context.Context, p commands.ProviderRun) error {
		named = p.Provider.Name()
		if p.Preflight.Project == nil {
			t.Error("the work was handed no resolved project for a command that required infrastructure")
		}
		return refused
	})
	if err == nil {
		t.Fatal("WithProvider err = nil, want the work's error to fail the run")
	}
	if !strings.Contains(rendered.String(), refused.Error()) {
		t.Errorf("the run rendered %q, want the work's error in its result", rendered.String())
	}
	if named == "" {
		t.Error("the work was handed no provider")
	}
}

func TestWithProviderRefusesAProjectNamingNoProviderBeforeAnyWork(t *testing.T) {
	invocation := clitest.NewInvocation()
	worked := false
	err := invocation.WithProvider(context.Background(), &project.Project{Slug: "no-provider", Dir: t.TempDir(), Path: "ocel.json"}, "ocel test", commands.OpenOptions{}, func(context.Context, commands.ProviderRun) error {
		worked = true
		return nil
	})
	if err == nil {
		t.Fatal("WithProvider err = nil, want a project naming no provider refused")
	}
	if worked {
		t.Error("the work ran for a project naming no provider")
	}
}
