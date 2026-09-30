package commands_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/environment"
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

func previewBootstrapSetup(fixture clitest.FakeProject, t *testing.T, runs *int) prerequisite.Setup {
	return prerequisite.Setup{
		Prompt:         func(missing prerequisite.MissingError) string { return "Run " + missing.Remedy() + " now?" },
		ChangesAccount: true,
		Run: func(context.Context, consent.Policy, *run.Span, prerequisite.MissingError) error {
			*runs++
			clitest.Bootstrap(t, fixture.Provider, environment.TierPreview)
			return nil
		},
	}
}

func TestWithProviderSetsUpWhatTheTierIsMissingAndChecksItAgain(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	invocation := clitest.NewInvocation()
	runs := 0
	invocation.Setups = prerequisite.Setups{prerequisite.Bootstrap: previewBootstrapSetup(fixture, t, &runs)}
	cfg, err := invocation.LoadProject(context.Background(), fixture.Root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	var asked strings.Builder
	policy := consent.Policy{Interactive: true, In: strings.NewReader("y\n"), Out: &asked}

	worked := false
	err = invocation.WithProvider(context.Background(), cfg, "ocel test", commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Infrastructure, Policy: policy}, func(_ context.Context, p commands.ProviderRun) error {
		worked = p.Project != nil && p.Project.Slug == cfg.Slug
		return nil
	})
	if err != nil {
		t.Fatalf("WithProvider err = %v, want the preview bootstrap set up and the work run", err)
	}
	if runs != 1 || !worked {
		t.Errorf("setup ran %d times and the work ran with the project: %t, want once and true", runs, worked)
	}
	if !strings.Contains(asked.String(), "Run `ocel bootstrap preview` now?") {
		t.Errorf("asked %q, want the bootstrap offered", asked.String())
	}
}

func TestWithProviderEndsADeclinedSetupAsARunThatChangedNothing(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	invocation := clitest.NewInvocation()
	var rendered strings.Builder
	clitest.AttachTerminalSink(invocation, &rendered)
	runs := 0
	invocation.Setups = prerequisite.Setups{prerequisite.Bootstrap: previewBootstrapSetup(fixture, t, &runs)}
	cfg, err := invocation.LoadProject(context.Background(), fixture.Root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	var asked strings.Builder
	policy := consent.Policy{Interactive: true, In: strings.NewReader("n\n"), Out: &asked}

	worked := false
	err = invocation.WithProvider(context.Background(), cfg, "ocel test", commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Infrastructure, Policy: policy}, func(context.Context, commands.ProviderRun) error {
		worked = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithProvider err = %v, want a declined setup to exit 0", err)
	}
	if runs != 0 || worked {
		t.Errorf("setup ran %d times and the work ran: %t, want neither", runs, worked)
	}
	if want := "Not set up, so this run changes nothing. When you're ready: `ocel bootstrap preview`"; !strings.Contains(rendered.String(), want) {
		t.Errorf("rendered %q, want %q", rendered.String(), want)
	}
}

func TestEnsureProjectSetsUpAMissingConfigAndLoadsIt(t *testing.T) {
	dir := t.TempDir()
	invocation := clitest.NewInvocation()
	invocation.Setups = prerequisite.Setups{prerequisite.Project: {
		Prompt: func(prerequisite.MissingError) string { return "Set up a project here?" },
		Run: func(context.Context, consent.Policy, *run.Span, prerequisite.MissingError) error {
			return os.WriteFile(filepath.Join(dir, project.DefaultFileName), []byte(`{"slug": "shop", "provider": {"fake": {}}}`), 0o644)
		},
	}}
	var asked strings.Builder
	policy := consent.Policy{Interactive: true, In: strings.NewReader("y\n"), Out: &asked}

	cfg, err := invocation.EnsureProject(context.Background(), dir, policy)
	if err != nil {
		t.Fatalf("EnsureProject err = %v, want the project set up and loaded", err)
	}
	if cfg.Slug != "shop" {
		t.Errorf("slug = %q, want the project the setup wrote", cfg.Slug)
	}

	t.Run("a declined setup is reported as one", func(t *testing.T) {
		policy := consent.Policy{Interactive: true, In: strings.NewReader("n\n"), Out: &asked}
		if _, err := invocation.EnsureProject(context.Background(), t.TempDir(), policy); !prerequisite.IsDeclined(err) {
			t.Errorf("EnsureProject err = %v, want the decline", err)
		}
	})
}
