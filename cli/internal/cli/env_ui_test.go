package cli

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func withRunnerValues(t *testing.T, root string, opts envOptions, drive func(ctx context.Context, slug string, runner *provider.Runner, values runnerValues) error) {
	t.Helper()
	ctx := context.Background()
	err := envSession(ctx, newSession(), root, opts, io.Discard, io.Discard, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		return drive(ctx, cfg.Slug, runner, runnerValues{
			runner: runner,
			slug:   cfg.Slug,
			tier:   envTier(opts),
		})
	})
	if err != nil {
		t.Fatalf("envSession: %v", err)
	}
}

func storeValue(t *testing.T, ctx context.Context, runner *provider.Runner, tier environmentv1.Tier, coordinate *envvarsv1.Coordinate, value string) {
	t.Helper()
	vars, err := runner.Vars()
	if err != nil {
		t.Fatalf("reach the provider's variable store: %v", err)
	}
	if _, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:       tier,
		Coordinate: coordinate,
		Value:      value,
	}); err != nil {
		t.Fatalf("SetValue %v: %v", coordinate, err)
	}
}

func revealOne(ctx context.Context, values runnerValues, cell envgate.Cell) (string, bool, error) {
	found, err := values.Reveal(ctx, []envgate.Address{{Cell: cell}})
	if err != nil {
		return "", false, err
	}
	value, ok := found[cell]
	return value, ok, nil
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

func TestRunnerValues(t *testing.T) {
	t.Run("List carries a named environment's value as an override", func(t *testing.T) {
		root := setUpEnvFixture(t)
		t.Setenv(clitest.FakeInfraClassEnvVar, "preview")
		preview := envOptions{preview: true}

		withRunnerValues(t, root, preview, func(ctx context.Context, slug string, runner *provider.Runner, values runnerValues) error {
			storeValue(t, ctx, runner, envTier(preview), &envvarsv1.Coordinate{Slug: slug, Key: "API_URL"}, "https://root.example")
			storeValue(t, ctx, runner, envTier(preview), &envvarsv1.Coordinate{Slug: slug, Key: "STRIPE_API_KEY", Environment: "staging"}, "sk_pr")

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
			if override.Environment != "staging" {
				t.Errorf("STRIPE_API_KEY = %+v, want it to name the environment that holds it", override)
			}
			return nil
		})
	})

	t.Run("Set against a stale version is refused as a stale value", func(t *testing.T) {
		root := setUpEnvFixture(t)

		withRunnerValues(t, root, envOptions{}, func(ctx context.Context, slug string, runner *provider.Runner, values runnerValues) error {
			storeValue(t, ctx, runner, envTier(envOptions{}), &envvarsv1.Coordinate{Slug: slug, Key: "API_URL"}, "https://someone-elses.example")
			at := envgate.Address{Cell: envgate.Cell{Key: "API_URL"}}

			unset := int64(0)
			if err := values.Set(ctx, at, "https://mine.example", &unset); !errors.Is(err, varsui.ErrStaleValue) {
				t.Fatalf("Set expecting an empty cell err = %v, want varsui.ErrStaleValue — the page drew a cell somebody has since filled", err)
			}
			if got, _, err := revealOne(ctx, values, at.Cell); err != nil || got != "https://someone-elses.example" {
				t.Errorf("the cell holds %q (err %v), want the value already there — a refused write must not have landed", got, err)
			}

			current := int64(1)
			if err := values.Set(ctx, at, "https://mine.example", &current); err != nil {
				t.Fatalf("Set expecting the current version err = %v, want the write to land", err)
			}
			if got, _, err := revealOne(ctx, values, at.Cell); err != nil || got != "https://mine.example" {
				t.Errorf("the cell holds %q (err %v), want the write that quoted the right version", got, err)
			}
			return nil
		})
	})

	t.Run("Delete against a stale version is refused as a stale value", func(t *testing.T) {
		root := setUpEnvFixture(t)

		withRunnerValues(t, root, envOptions{}, func(ctx context.Context, slug string, runner *provider.Runner, values runnerValues) error {
			coordinate := &envvarsv1.Coordinate{Slug: slug, Key: "API_URL"}
			storeValue(t, ctx, runner, envTier(envOptions{}), coordinate, "https://first.example")
			storeValue(t, ctx, runner, envTier(envOptions{}), coordinate, "https://someone-elses.example")
			at := envgate.Address{Cell: envgate.Cell{Key: "API_URL"}}

			rendered := int64(1)
			if err := values.Delete(ctx, at, &rendered); !errors.Is(err, varsui.ErrStaleValue) {
				t.Fatalf("Delete expecting version 1 err = %v, want varsui.ErrStaleValue — the page drew a value somebody has since replaced", err)
			}
			if got, found, err := revealOne(ctx, values, at.Cell); err != nil || !found || got != "https://someone-elses.example" {
				t.Errorf("the cell holds %q (found %v, err %v), want the replacement — a refused delete must not have landed", got, found, err)
			}

			current := int64(2)
			if err := values.Delete(ctx, at, &current); err != nil {
				t.Fatalf("Delete expecting the current version err = %v, want the delete to land", err)
			}
			if _, found, err := revealOne(ctx, values, at.Cell); err != nil || found {
				t.Errorf("the cell is still set (found %v, err %v), want the honoured delete to have unset it", found, err)
			}
			return nil
		})
	})
}
