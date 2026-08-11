package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestLockRecoveryHint(t *testing.T) {
	t.Parallel()

	t.Run("names the release command for a stale lock only", func(t *testing.T) {
		t.Parallel()

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
		realized := &realizedStacks{}
		if err := realized.realize(context.Background(), &fakeStackIndex{}, "shop--web--b1"); err != nil {
			t.Fatalf("realize: %v", err)
		}
		if !applied(TeardownConfig{StackName: "shop--infra", realized: realized}, nil).Refresh {
			t.Error("a stack realized elsewhere skipped the refresh")
		}
		if applied(TeardownConfig{StackName: "shop--web--b1", realized: realized}, nil).Refresh {
			t.Error("a stack this session realized still refreshed")
		}
	})

	t.Run("skips the refresh the caller opted out of", func(t *testing.T) {
		t.Parallel()

		if applied(TeardownConfig{StackName: "shop--infra", SkipRefresh: true}, nil).Refresh {
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

	err := Destroy(context.Background(), TeardownConfig{StackName: "shop--infra"}, nil, nil)
	if !errors.Is(err, errNoStackIndex) {
		t.Fatalf("Destroy err = %v, want %v raised before the stack is touched", err, errNoStackIndex)
	}
}

func TestTeardownConfigCarriesTheSession(t *testing.T) {
	t.Parallel()

	realized := &realizedStacks{}
	if err := realized.realize(context.Background(), &fakeStackIndex{}, "shop--infra"); err != nil {
		t.Fatalf("realize: %v", err)
	}
	if !teardownConfig(Config{realized: realized}, "shop--infra").realized.realizedHere("shop--infra") {
		t.Error("teardownConfig dropped the session, so a fresh stack would refresh needlessly")
	}
}

func TestTeardownConfigSkipRefresh(t *testing.T) {
	t.Run("refreshes unless the environment opts out", func(t *testing.T) {
		if teardownConfig(Config{}, "shop--infra").SkipRefresh {
			t.Error("teardownConfig skipped the refresh with no opt-out set")
		}
		for _, v := range []string{"1", "true", "TRUE", "True"} {
			t.Setenv(skipTeardownRefreshEnv, v)
			if !teardownConfig(Config{}, "shop--infra").SkipRefresh {
				t.Errorf("%s=%q did not skip the refresh", skipTeardownRefreshEnv, v)
			}
		}
		for _, v := range []string{"", "0", "false", "FALSE", "yes"} {
			t.Setenv(skipTeardownRefreshEnv, v)
			if teardownConfig(Config{}, "shop--infra").SkipRefresh {
				t.Errorf("%s=%q skipped the refresh, want only \"1\" and \"true\" to", skipTeardownRefreshEnv, v)
			}
		}
	})
}

func TestPreviewStacksFromNames(t *testing.T) {
	t.Parallel()

	t.Run("one entry per pointer with inferred lifecycle", func(t *testing.T) {
		t.Parallel()

		got := previewStacksFromNames("shop", []string{
			PreviewInfraStackName("shop", "staging"),
			PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1")),
			PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b2")),
			PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b3")),
			PreviewAppDeployStackName("shop", "pr-1", "api", buildOnly("b4")),
			InfraStackName("shop"),
			AppDeployStackName("shop", "web", buildOnly("b9")),
			"other--preview-x--web--b1",
			"shop-preview-legacy",
			"shopfoo--preview-y--web--b1",
		})

		want := []PreviewStack{
			{Identity: "pr-1", Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL},
			{Identity: "staging", Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("previewStacksFromNames = %+v, want %+v", got, want)
		}
	})

	t.Run("retired shape and foreign projects excluded", func(t *testing.T) {
		t.Parallel()

		got := previewStacksFromNames("shop", []string{
			"shop-preview-feature_login_ab12",
			"other--preview-x--web--b1",
			InfraStackName("shop"),
			AppDeployStackName("shop", "web", buildOnly("b1")),
		})
		if len(got) != 0 {
			t.Errorf("previewStacksFromNames matched retired/foreign/production stacks: %+v", got)
		}
	})
}
