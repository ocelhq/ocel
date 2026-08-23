package deploy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

func TestRefreshPolicy(t *testing.T) {
	web := naming.AppStack("prod", "web", testRelease(t, "b1"))
	realized := &Realized{}
	if err := realized.realize(context.Background(), &fakeStackIndex{}, "shop", web); err != nil {
		t.Fatalf("realize: %v", err)
	}
	refreshes := refreshPolicy(realized)
	ref := func(project string, stack naming.StackName) providerkit.StackRef {
		return providerkit.StackRef{Project: project, Name: stack}
	}

	t.Run("a provision never refreshes, because the run just wrote the state it would read", func(t *testing.T) {
		if refreshes(ref("shop", naming.InfraStack("prod")), kitpulumi.OpProvision) {
			t.Error("a provision refreshed, and a refresh costs a full read of the account")
		}
	})

	t.Run("a teardown refreshes a stack this session did not realize", func(t *testing.T) {
		if !refreshes(ref("shop", naming.InfraStack("prod")), kitpulumi.OpDestroy) {
			t.Error("a stack realized elsewhere skipped the refresh")
		}
		if refreshes(ref("shop", web), kitpulumi.OpDestroy) {
			t.Error("a stack this session realized still refreshed")
		}
		if !refreshes(ref("othr", web), kitpulumi.OpDestroy) {
			t.Error("another project's stack of the same name skipped the refresh")
		}
	})

	t.Run("the environment can opt every teardown out", func(t *testing.T) {
		for _, v := range []string{"1", "true", "TRUE", "True"} {
			t.Setenv(skipTeardownRefreshEnv, v)
			if refreshes(ref("shop", naming.InfraStack("prod")), kitpulumi.OpDestroy) {
				t.Errorf("%s=%q did not skip the refresh", skipTeardownRefreshEnv, v)
			}
		}
		for _, v := range []string{"", "0", "false", "FALSE", "yes"} {
			t.Setenv(skipTeardownRefreshEnv, v)
			if !refreshes(ref("shop", naming.InfraStack("prod")), kitpulumi.OpDestroy) {
				t.Errorf("%s=%q skipped the refresh, want only \"1\" and \"true\" to", skipTeardownRefreshEnv, v)
			}
		}
	})
}

func TestDestroyRefusesWithoutIndex(t *testing.T) {
	t.Parallel()

	err := Destroy(context.Background(), StackTeardown{Project: "shop", Stack: naming.InfraStack("prod")}, nil, nil)
	if !errors.Is(err, errNoStackIndex) {
		t.Fatalf("Destroy err = %v, want %v raised before the stack is touched", err, errNoStackIndex)
	}
}

func TestTeardownForStackCarriesTheSession(t *testing.T) {
	t.Parallel()

	infra := naming.InfraStack("prod")
	realized := &Realized{}
	if err := realized.realize(context.Background(), &fakeStackIndex{}, "shop", infra); err != nil {
		t.Fatalf("realize: %v", err)
	}
	cfg := Teardown{Slug: "shop", Realized: realized}.forStack(infra)
	if cfg.Project != "shop" {
		t.Errorf("forStack Project = %q, want the project the stack is indexed under", cfg.Project)
	}
	if !cfg.Realized.realizedHere("shop", infra) {
		t.Error("forStack dropped the session, so a fresh stack would refresh needlessly")
	}
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
			{Identity: "pr-1", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL},
			{Identity: "staging", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT},
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
