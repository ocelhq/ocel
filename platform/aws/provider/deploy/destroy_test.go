package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestLockRecoveryHint(t *testing.T) {
	t.Parallel()

	t.Run("names the release command for a stale lock only", func(t *testing.T) {
		t.Parallel()

		cfg := TeardownConfig{Stack: naming.InfraStack("pr-1"), BackendURL: "s3://state-bucket"}

		locked := errors.New("the stack is currently locked by 1 lock(s). Either wait for the other process(es) to end or delete the lock file with 'pulumi cancel'")
		hint := lockRecoveryHint(locked, cfg)
		for _, want := range []string{"pulumi cancel", cfg.Stack.String(), cfg.BackendURL} {
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
	})
}

func TestDestroyOptions(t *testing.T) {
	t.Parallel()

	applied := func(cfg TeardownConfig, w *lineForwarder) optdestroy.Options {
		var opts optdestroy.Options
		for _, o := range destroyOptions(cfg, w) {
			o.ApplyOption(&opts)
		}
		return opts
	}

	t.Run("refreshes a stack this session did not realize", func(t *testing.T) {
		t.Parallel()

		if !applied(TeardownConfig{}, nil).Refresh {
			t.Error("a zero TeardownConfig skipped the refresh")
		}
		web := naming.AppStack("prod", "web", testRelease(t, "b1"))
		realized := &realizedStacks{}
		if err := realized.realize(context.Background(), &fakeStackIndex{}, "shop", web); err != nil {
			t.Fatalf("realize: %v", err)
		}
		if !applied(TeardownConfig{Project: "shop", Stack: naming.InfraStack("prod"), realized: realized}, nil).Refresh {
			t.Error("a stack realized elsewhere skipped the refresh")
		}
		if applied(TeardownConfig{Project: "shop", Stack: web, realized: realized}, nil).Refresh {
			t.Error("a stack this session realized still refreshed")
		}
		if !applied(TeardownConfig{Project: "othr", Stack: web, realized: realized}, nil).Refresh {
			t.Error("another project's stack of the same name skipped the refresh")
		}
	})

	t.Run("skips the refresh the caller opted out of", func(t *testing.T) {
		t.Parallel()

		if applied(TeardownConfig{Stack: naming.InfraStack("prod"), SkipRefresh: true}, nil).Refresh {
			t.Error("an opted-out teardown still refreshed")
		}
	})

	t.Run("streams progress only with a log sink", func(t *testing.T) {
		t.Parallel()

		if got := applied(TeardownConfig{}, nil).ProgressStreams; len(got) != 0 {
			t.Errorf("progress streams = %v, want none with no log sink", got)
		}
		if got := applied(TeardownConfig{}, lineWriter(func(string) {})).ProgressStreams; len(got) != 1 {
			t.Errorf("progress streams = %v, want the log sink attached", got)
		}
	})
}

func TestDestroyRefusesWithoutIndex(t *testing.T) {
	t.Parallel()

	err := Destroy(context.Background(), TeardownConfig{Project: "shop", Stack: naming.InfraStack("prod")}, nil, nil)
	if !errors.Is(err, errNoStackIndex) {
		t.Fatalf("Destroy err = %v, want %v raised before the stack is touched", err, errNoStackIndex)
	}
}

func TestTeardownConfigCarriesTheSession(t *testing.T) {
	t.Parallel()

	infra := naming.InfraStack("prod")
	realized := &realizedStacks{}
	if err := realized.realize(context.Background(), &fakeStackIndex{}, "shop", infra); err != nil {
		t.Fatalf("realize: %v", err)
	}
	cfg := teardownConfig(Config{Slug: "shop", realized: realized}, infra)
	if cfg.Project != "shop" {
		t.Errorf("teardownConfig Project = %q, want the project the stack is indexed under", cfg.Project)
	}
	if !cfg.realized.realizedHere("shop", infra) {
		t.Error("teardownConfig dropped the session, so a fresh stack would refresh needlessly")
	}
}

func TestTeardownConfigSkipRefresh(t *testing.T) {
	infra := naming.InfraStack("prod")

	t.Run("refreshes unless the environment opts out", func(t *testing.T) {
		if teardownConfig(Config{}, infra).SkipRefresh {
			t.Error("teardownConfig skipped the refresh with no opt-out set")
		}
		for _, v := range []string{"1", "true", "TRUE", "True"} {
			t.Setenv(skipTeardownRefreshEnv, v)
			if !teardownConfig(Config{}, infra).SkipRefresh {
				t.Errorf("%s=%q did not skip the refresh", skipTeardownRefreshEnv, v)
			}
		}
		for _, v := range []string{"", "0", "false", "FALSE", "yes"} {
			t.Setenv(skipTeardownRefreshEnv, v)
			if teardownConfig(Config{}, infra).SkipRefresh {
				t.Errorf("%s=%q skipped the refresh, want only \"1\" and \"true\" to", skipTeardownRefreshEnv, v)
			}
		}
	})
}

func TestPreviewStacksFromNames(t *testing.T) {
	t.Parallel()

	t.Run("one entry per pointer with inferred lifecycle", func(t *testing.T) {
		t.Parallel()

		got := previewStacksFromNames([]naming.StackName{
			naming.InfraStack("staging"),
			naming.AppStack("staging", "web", testRelease(t, "b1")),
			naming.AppStack("pr-1", "web", testRelease(t, "b2")),
			naming.AppStack("pr-1", "web", testRelease(t, "b3")),
			naming.AppStack("pr-1", "api", testRelease(t, "b4")),
			naming.InfraStack(ProductionEnv),
			naming.AppStack(ProductionEnv, "web", testRelease(t, "b9")),
		})

		want := []PreviewStack{
			{Identity: "pr-1", Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL},
			{Identity: "staging", Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("previewStacksFromNames = %+v, want %+v", got, want)
		}
	})

	t.Run("production stacks excluded", func(t *testing.T) {
		t.Parallel()

		got := previewStacksFromNames([]naming.StackName{
			naming.InfraStack(ProductionEnv),
			naming.AppStack(ProductionEnv, "web", testRelease(t, "b1")),
		})
		if len(got) != 0 {
			t.Errorf("previewStacksFromNames matched production stacks: %+v", got)
		}
	})
}
