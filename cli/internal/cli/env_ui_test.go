package cli

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
)

// withRunnerValues drives the store the way the variables UI does: over a live
// provider session, through the same runnerValues the gate and the UI share.
func withRunnerValues(t *testing.T, root string, drive func(ctx context.Context, slug string, runner *providerrunner.Runner, values runnerValues) error) {
	t.Helper()
	ctx := context.Background()
	err := envSession(ctx, root, envOptions{}, io.Discard, io.Discard, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		return drive(ctx, cfg.Slug, runner, runnerValues{
			runner:  runner,
			options: []byte(provider.Options),
			slug:    cfg.Slug,
			class:   envClass(envOptions{}),
		})
	})
	if err != nil {
		t.Fatalf("envSession: %v", err)
	}
}

func storeValue(t *testing.T, ctx context.Context, runner *providerrunner.Runner, coordinate *envv1.Coordinate, value string) {
	t.Helper()
	if _, err := runner.SetValue(ctx, &envv1.SetValueRequest{
		ProtocolVersion: manifestbuilder.SchemaVersion,
		Class:           envClass(envOptions{}),
		Coordinate:      coordinate,
		Value:           value,
	}); err != nil {
		t.Fatalf("SetValue %v: %v", coordinate, err)
	}
}

func stored(t *testing.T, rows []envgate.Stored, key string) envgate.Stored {
	t.Helper()
	for _, row := range rows {
		if row.Cell.Key == key {
			return row
		}
	}
	t.Fatalf("List has no row for %q; rows are %+v", key, rows)
	return envgate.Stored{}
}

// A named environment's value is a value, and the gate has to be told it is
// there — but as an override, never as the class-wide value a deploy resolves.
// Discarding it is what let a deleted class-wide cell leave a survivor nothing
// could see.
func TestRunnerValues_ListCarriesANamedEnvironmentsValueAsAnOverride(t *testing.T) {
	root := setUpEnvFixture(t)

	withRunnerValues(t, root, func(ctx context.Context, slug string, runner *providerrunner.Runner, values runnerValues) error {
		storeValue(t, ctx, runner, &envv1.Coordinate{Slug: slug, Key: "API_URL"}, "https://root.example")
		storeValue(t, ctx, runner, &envv1.Coordinate{Slug: slug, Key: "STRIPE_API_KEY", Environment: "pr-42"}, "sk_pr")

		rows, err := values.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("List returned %+v, want both the class-wide value and the override", rows)
		}

		classWide := stored(t, rows, "API_URL")
		if classWide.Environment != "" || classWide.Version != 1 {
			t.Errorf("API_URL = %+v, want no environment and version 1", classWide)
		}
		override := stored(t, rows, "STRIPE_API_KEY")
		if override.Environment != "pr-42" {
			t.Errorf("STRIPE_API_KEY = %+v, want it to name the environment that holds it", override)
		}
		return nil
	})
}

// The version the page rendered is the write's expectation, and a write whose
// expectation no longer holds is refused rather than applied — otherwise two
// people editing one cell both succeed and the second silently wins.
func TestRunnerValues_SetAgainstAStaleVersionIsRefusedAsAStaleValue(t *testing.T) {
	root := setUpEnvFixture(t)

	withRunnerValues(t, root, func(ctx context.Context, slug string, runner *providerrunner.Runner, values runnerValues) error {
		storeValue(t, ctx, runner, &envv1.Coordinate{Slug: slug, Key: "API_URL"}, "https://someone-elses.example")
		cell := envgate.Cell{Key: "API_URL"}

		unset := int64(0)
		if err := values.Set(ctx, cell, "https://mine.example", &unset); !errors.Is(err, varsui.ErrStaleValue) {
			t.Fatalf("Set expecting an empty cell err = %v, want varsui.ErrStaleValue — the page drew a cell somebody has since filled", err)
		}
		if got, _, err := values.Reveal(ctx, cell); err != nil || got != "https://someone-elses.example" {
			t.Errorf("the cell holds %q (err %v), want the value already there — a refused write must not have landed", got, err)
		}

		current := int64(1)
		if err := values.Set(ctx, cell, "https://mine.example", &current); err != nil {
			t.Fatalf("Set expecting the current version err = %v, want the write to land", err)
		}
		if got, _, err := values.Reveal(ctx, cell); err != nil || got != "https://mine.example" {
			t.Errorf("the cell holds %q (err %v), want the write that quoted the right version", got, err)
		}
		return nil
	})
}

// Remove carries the same expectation Save does. Two developers hold the page
// open, one replaces the value, and the other's Remove must lose to that write
// rather than destroy it with nothing on either page admitting it happened.
func TestRunnerValues_DeleteAgainstAStaleVersionIsRefusedAsAStaleValue(t *testing.T) {
	root := setUpEnvFixture(t)

	withRunnerValues(t, root, func(ctx context.Context, slug string, runner *providerrunner.Runner, values runnerValues) error {
		coordinate := &envv1.Coordinate{Slug: slug, Key: "API_URL"}
		storeValue(t, ctx, runner, coordinate, "https://first.example")
		storeValue(t, ctx, runner, coordinate, "https://someone-elses.example")
		cell := envgate.Cell{Key: "API_URL"}

		rendered := int64(1)
		if err := values.Delete(ctx, cell, &rendered); !errors.Is(err, varsui.ErrStaleValue) {
			t.Fatalf("Delete expecting version 1 err = %v, want varsui.ErrStaleValue — the page drew a value somebody has since replaced", err)
		}
		if got, found, err := values.Reveal(ctx, cell); err != nil || !found || got != "https://someone-elses.example" {
			t.Errorf("the cell holds %q (found %v, err %v), want the replacement — a refused delete must not have landed", got, found, err)
		}

		current := int64(2)
		if err := values.Delete(ctx, cell, &current); err != nil {
			t.Fatalf("Delete expecting the current version err = %v, want the delete to land", err)
		}
		if _, found, err := values.Reveal(ctx, cell); err != nil || found {
			t.Errorf("the cell is still set (found %v, err %v), want the honoured delete to have unset it", found, err)
		}
		return nil
	})
}
