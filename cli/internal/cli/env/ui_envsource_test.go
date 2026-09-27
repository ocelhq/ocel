package env

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	"github.com/ocelhq/ocel/pkg/envsource"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

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

func withVarsUI(t *testing.T, root string, drive func(s *varsui.Session)) {
	t.Helper()
	ctx := context.Background()
	err := withEnvProvider(ctx, clitest.NewDeps(), root, envOptions{}, io.Discard, func(runner *providerclient.Runner, cfg *projectconfig.Config, _ *contractv1.PreflightResponse) error {
		gate, err := discoverVariables(ctx, cfg, runner, envOptions{}, io.Discard)
		if err != nil {
			return err
		}
		s, err := envwire.ServeVarsUI(ctx, cfg, runner, false, gate, nil)
		if err != nil {
			return err
		}
		defer s.Close()
		drive(s)
		return nil
	})
	if err != nil {
		t.Fatalf("serve the variables UI: %v", err)
	}
}

func callVarsUI(t *testing.T, s *varsui.Session, method, path string, body any) *http.Response {
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

func varsUIState(t *testing.T, s *varsui.Session) varsui.State {
	t.Helper()
	res := callVarsUI(t, s, http.MethodGet, "/api/state", nil)
	var out varsui.State
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode the page's state (%d): %v", res.StatusCode, err)
	}
	return out
}

func matrixRow(state varsui.State, key string) (envgate.MatrixRow, bool) {
	i := slices.IndexFunc(state.Matrix.Rows, func(row envgate.MatrixRow) bool { return row.Key == key })
	if i < 0 {
		return envgate.MatrixRow{}, false
	}
	return state.Matrix.Rows[i], true
}

func TestEnvUINamesTheEnvSourceAndWhatItCopied(t *testing.T) {
	root := syncedEnvSourceFixture(t, infisicalProduction)
	withVarsUI(t, root, func(s *varsui.Session) {
		state := varsUIState(t, s)
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
		if !declared || credential.Group != envgate.EnvSourceGroup || credential.Class != "secret" {
			t.Errorf("INFISICAL_CLIENT_SECRET = %+v (declared %v), want a secret in the %q group", credential, declared, envgate.EnvSourceGroup)
		}
		want := envgate.UndeclaredCell{Cell: envgate.Cell{Key: "RETIRED"}, EnvSource: "infisical:p-1/prod"}
		if !slices.Contains(state.Matrix.Undeclared, want) {
			t.Errorf("undeclared = %+v, want RETIRED, which nothing declares", state.Matrix.Undeclared)
		}
	})
}

func TestEnvUICreatesAValueTheEnvSourceLacksThere(t *testing.T) {
	writable := infisicalProduction
	options := *writable.Infisical
	options.Write = envsource.WriteMissing
	writable.Infisical = &options
	root := syncedEnvSourceFixture(t, writable)

	withVarsUI(t, root, func(s *varsui.Session) {
		res := callVarsUI(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "API_TOKEN", "value": "tok"})
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("POST = %d: %s", res.StatusCode, body)
		}
		token, _ := matrixRow(varsUIState(t, s), "API_TOKEN")
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
