package env

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/projecteditor"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/envsource"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func withProviderValues(t *testing.T, root string, opts envOptions, drive func(ctx context.Context, slug string, prov *providerclient.Provider, values valuestore.Store) error) {
	t.Helper()
	err := withEnvProvider(context.Background(), clitest.NewDeps(), root, opts, "ocel env", io.Discard, func(ctx context.Context, _ *run.Run, prov *providerclient.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		return drive(ctx, cfg.Slug, prov, valuestore.Store{
			Provider: prov,
			Project:  cfg,
			Tier:     opts.tier(),
		})
	})
	if err != nil {
		t.Fatalf("withEnvProvider: %v", err)
	}
}

func storeValue(t *testing.T, ctx context.Context, prov *providerclient.Provider, tier environmentv1.Tier, coordinate *envvarsv1.Coordinate, value string) {
	t.Helper()
	vars, err := prov.Vars()
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

func revealOne(ctx context.Context, values valuestore.Store, cell variables.Cell) (string, bool, error) {
	found, err := values.Reveal(ctx, []variables.Coordinate{{Cell: cell}})
	if err != nil {
		return "", false, err
	}
	value, ok := found[variables.Coordinate{Cell: cell}]
	return value, ok, nil
}

func stored(t *testing.T, rows []variables.ValueMetadata, key string) variables.ValueMetadata {
	t.Helper()
	for _, row := range rows {
		if row.Cell.Key == key {
			return row
		}
	}
	t.Fatalf("List has no row for %q; rows are %+v", key, rows)
	return variables.ValueMetadata{}
}

func TestTheValuesTheVariablesPageShowsAndChangesAreTheProvidersAnswers(t *testing.T) {
	t.Run("List includes a named environment's value as an override", func(t *testing.T) {
		root := setUpEnvFixture(t)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		preview := envOptions{preview: true}

		withProviderValues(t, root, preview, func(ctx context.Context, slug string, prov *providerclient.Provider, values valuestore.Store) error {
			storeValue(t, ctx, prov, preview.tier(), &envvarsv1.Coordinate{Slug: slug, Key: "API_URL"}, "https://root.example")
			storeValue(t, ctx, prov, preview.tier(), &envvarsv1.Coordinate{Slug: slug, Key: "STRIPE_API_KEY", Environment: "staging"}, "sk_pr")

			rows, err := values.List(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(rows) != 2 {
				t.Fatalf("List returned %+v, want both the value bound to all environments and the override", rows)
			}

			base := stored(t, rows, "API_URL")
			if base.Environment != "" || base.Version != 1 {
				t.Errorf("API_URL = %+v, want no environment and version 1", base)
			}
			override := stored(t, rows, "STRIPE_API_KEY")
			if override.Environment != "staging" {
				t.Errorf("STRIPE_API_KEY = %+v, want it to name the environment that stores it", override)
			}
			return nil
		})
	})

	t.Run("a refused reveal hands back the provider's own typed error", func(t *testing.T) {
		root := setUpEnvFixture(t)

		withProviderValues(t, root, envOptions{}, func(ctx context.Context, slug string, prov *providerclient.Provider, values valuestore.Store) error {
			vars, err := prov.Vars()
			if err != nil {
				t.Fatalf("reach the provider's variable store: %v", err)
			}
			if _, err := vars.SetReference(ctx, &envvarsv1.SetReferenceRequest{
				Tier:       envOptions{}.tier(),
				Coordinate: &envvarsv1.Coordinate{Slug: slug, Key: "STRIPE_API_KEY"},
				Target:     &envvarsv1.Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"},
			}); err != nil {
				t.Fatalf("SetReference: %v", err)
			}

			_, direct := vars.RevealValues(ctx, &envvarsv1.RevealValuesRequest{
				Tier:  envOptions{}.tier(),
				Slug:  slug,
				Cells: []*envvarsv1.Coordinate{{Slug: slug, Key: "STRIPE_API_KEY"}},
			})

			_, _, err = revealOne(ctx, values, variables.Cell{Key: "STRIPE_API_KEY"})
			var wire *connect.Error
			if !errors.As(err, &wire) {
				t.Fatalf("Reveal over a reference to nothing err = %v, want a *connect.Error a caller can read the code off", err)
			}
			if wire.Code() != connect.CodeOf(direct) {
				t.Errorf("Reveal err code = %v, want %v: the code the provider answered with", wire.Code(), connect.CodeOf(direct))
			}
			return nil
		})
	})

	t.Run("Set against a stale version is refused as a stale value", func(t *testing.T) {
		root := setUpEnvFixture(t)

		withProviderValues(t, root, envOptions{}, func(ctx context.Context, slug string, prov *providerclient.Provider, values valuestore.Store) error {
			storeValue(t, ctx, prov, envOptions{}.tier(), &envvarsv1.Coordinate{Slug: slug, Key: "API_URL"}, "https://someone-elses.example")
			at := variables.Coordinate{Cell: variables.Cell{Key: "API_URL"}}

			unset := int64(0)
			if _, err := values.Set(ctx, at, "https://mine.example", &unset); !errors.Is(err, variables.ErrStaleValue) {
				t.Fatalf("Set expecting an empty cell err = %v, want variables.ErrStaleValue — the page drew a cell somebody has since filled", err)
			}
			if got, _, err := revealOne(ctx, values, at.Cell); err != nil || got != "https://someone-elses.example" {
				t.Errorf("the cell contains %q (err %v), want the value already there — a refused write must not have landed", got, err)
			}

			current := int64(1)
			if _, err := values.Set(ctx, at, "https://mine.example", &current); err != nil {
				t.Fatalf("Set expecting the current version err = %v, want the write to land", err)
			}
			if got, _, err := revealOne(ctx, values, at.Cell); err != nil || got != "https://mine.example" {
				t.Errorf("the cell contains %q (err %v), want the write that quoted the right version", got, err)
			}
			return nil
		})
	})

	t.Run("Delete against a stale version is refused as a stale value", func(t *testing.T) {
		root := setUpEnvFixture(t)

		withProviderValues(t, root, envOptions{}, func(ctx context.Context, slug string, prov *providerclient.Provider, values valuestore.Store) error {
			coordinate := &envvarsv1.Coordinate{Slug: slug, Key: "API_URL"}
			storeValue(t, ctx, prov, envOptions{}.tier(), coordinate, "https://first.example")
			storeValue(t, ctx, prov, envOptions{}.tier(), coordinate, "https://someone-elses.example")
			at := variables.Coordinate{Cell: variables.Cell{Key: "API_URL"}}

			rendered := int64(1)
			if _, err := values.Delete(ctx, at, &rendered); !errors.Is(err, variables.ErrStaleValue) {
				t.Fatalf("Delete expecting version 1 err = %v, want variables.ErrStaleValue — the page drew a value somebody has since replaced", err)
			}
			if got, found, err := revealOne(ctx, values, at.Cell); err != nil || !found || got != "https://someone-elses.example" {
				t.Errorf("the cell contains %q (found %v, err %v), want the replacement — a refused delete must not have landed", got, found, err)
			}

			current := int64(2)
			if _, err := values.Delete(ctx, at, &current); err != nil {
				t.Fatalf("Delete expecting the current version err = %v, want the delete to land", err)
			}
			if _, found, err := revealOne(ctx, values, at.Cell); err != nil || found {
				t.Errorf("the cell is still set (found %v, err %v), want the honoured delete to have unset it", found, err)
			}
			return nil
		})
	})
}

func syncedEnvSourceFixture(t *testing.T, descriptor envsource.Descriptor) string {
	t.Helper()
	root := setUpEnvSourceFixture(t, clitest.FakeEnvSource{
		Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk"}, {Key: "RETIRED", Value: "old"}},
		URLs:   map[string]string{"": "https://infisical.example/prod"},
	})
	setCredentials(t, root)
	registerFakeEnvSource(t, environmentv1.Tier_TIER_PRODUCTION, descriptor)
	var synced bytes.Buffer
	if err := runEnvSync(context.Background(), clitest.NewDeps(), root, envOptions{}, &synced, &synced); err != nil {
		t.Fatalf("runEnvSync err = %v; out=%s", err, synced.String())
	}
	return root
}

func withEditor(t *testing.T, root string, drive func(s *variableeditor.Session)) {
	t.Helper()
	err := withEnvProvider(context.Background(), clitest.NewDeps(), root, envOptions{}, "ocel env ui", io.Discard, func(ctx context.Context, run *run.Run, prov *providerclient.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		declarations, err := discoverVariables(ctx, cfg, prov, envOptions{}, run)
		if err != nil {
			return err
		}
		s, err := projecteditor.Serve(ctx, cfg, prov, environmentv1.Tier_TIER_PRODUCTION, declarations, nil)
		if err != nil {
			return err
		}
		defer s.Close()
		drive(s)
		return nil
	})
	if err != nil {
		t.Fatalf("serve the variable editor: %v", err)
	}
}

func callEditor(t *testing.T, s *variableeditor.Session, method, path string, body any) *http.Response {
	t.Helper()
	origin := strings.TrimSuffix(strings.SplitN(s.URL, "#", 2)[0], "/")
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, origin+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Origin", origin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func editorPage(t *testing.T, s *variableeditor.Session) variableeditor.Page {
	t.Helper()
	res := callEditor(t, s, http.MethodGet, "/api/state", nil)
	var out variableeditor.Page
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode the page's state (%d): %v", res.StatusCode, err)
	}
	return out
}

func matrixRow(state variableeditor.Page, key string) (variables.MatrixRow, bool) {
	i := slices.IndexFunc(state.Matrix.Rows, func(row variables.MatrixRow) bool { return row.Key == key })
	if i < 0 {
		return variables.MatrixRow{}, false
	}
	return state.Matrix.Rows[i], true
}

func TestEnvUINamesTheEnvSourceAndWhatItCopied(t *testing.T) {
	root := syncedEnvSourceFixture(t, infisicalProduction)
	withEditor(t, root, func(s *variableeditor.Session) {
		state := editorPage(t, s)
		if state.EnvSource == nil || state.EnvSource.ID != "infisical:p-1/prod" || state.EnvSource.URLs[""] != "https://infisical.example/prod" {
			t.Fatalf("env source = %+v, want infisical named with its root URL", state.EnvSource)
		}
		if !slices.Equal(state.EnvSource.Credentials, []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"}) {
			t.Errorf("credentials = %q, want both universal auth variables", state.EnvSource.Credentials)
		}
		stripe, _ := matrixRow(state, "STRIPE_API_KEY")
		if len(stripe.Cells) == 0 || stripe.Cells[0].EnvSource != "infisical:p-1/prod" {
			t.Errorf("STRIPE_API_KEY = %+v, want its value's env source named", stripe)
		}
		credential, declared := matrixRow(state, "INFISICAL_CLIENT_SECRET")
		if !declared || credential.Group != variables.EnvSourceGroup || credential.Class != "secret" {
			t.Errorf("INFISICAL_CLIENT_SECRET = %+v (declared %v), want a secret in the %q group", credential, declared, variables.EnvSourceGroup)
		}
		want := variables.UndeclaredCell{Cell: variables.Cell{Key: "RETIRED"}, EnvSource: "infisical:p-1/prod"}
		if !slices.Contains(state.Matrix.Undeclared, want) {
			t.Errorf("undeclared = %+v, want RETIRED, which nothing declares", state.Matrix.Undeclared)
		}
	})
}

func TestEnvUIUpdatesAValueTheEnvSourceHoldsUnderWriteValues(t *testing.T) {
	root := syncedEnvSourceFixture(t, writing(envsource.WriteValues))

	withEditor(t, root, func(s *variableeditor.Session) {
		if source := editorPage(t, s).EnvSource; source == nil || !source.CanCreate || !source.CanUpdate {
			t.Fatalf("env source = %+v, want one ocel may create and update values in", source)
		}
		res := callEditor(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "STRIPE_API_KEY", "value": "sk_rotated"})
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("POST = %d: %s", res.StatusCode, body)
		}
	})
	if updated := productionRegistration(t).Updated; !slices.Contains(updated, clitest.FakeEnvSourceValue{Key: "STRIPE_API_KEY", Value: "sk_rotated"}) {
		t.Errorf("updated %+v, want STRIPE_API_KEY updated in the env source", updated)
	}
}

func TestEnvUICreatesAValueTheEnvSourceLacksThere(t *testing.T) {
	writable := infisicalProduction
	options := *writable.Infisical
	options.Write = envsource.WriteMissing
	writable.Infisical = &options
	root := syncedEnvSourceFixture(t, writable)

	withEditor(t, root, func(s *variableeditor.Session) {
		res := callEditor(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "API_TOKEN", "value": "tok"})
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("POST = %d: %s", res.StatusCode, body)
		}
		token, _ := matrixRow(editorPage(t, s), "API_TOKEN")
		if len(token.Cells) == 0 || !token.Cells[0].Set || token.Cells[0].EnvSource != "infisical:p-1/prod" {
			t.Errorf("API_TOKEN = %+v, want it set from the env source", token)
		}
	})
	registrations, err := clitest.LoadFakeRegistrations()
	if err != nil {
		t.Fatal(err)
	}
	registration := registrations[clitest.FakeRegistrationKey(environmentv1.Tier_TIER_PRODUCTION, clitest.FixtureSlug)]
	if !slices.Contains(registration.Created, clitest.FakeEnvSourceValue{Key: "API_TOKEN", Value: "tok"}) {
		t.Errorf("created %+v, want API_TOKEN created in the env source", registration.Created)
	}
}
