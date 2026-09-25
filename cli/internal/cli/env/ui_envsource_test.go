package env

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func syncedSourceFixture(t *testing.T, writable bool) string {
	t.Helper()
	root := setUpSourcedFixture(t, clitest.FakeEnvSource{
		ID:     "infisical:p-1/prod",
		Values: []clitest.FakeSourced{{Key: "STRIPE_API_KEY", Value: "sk"}, {Key: "RETIRED", Value: "old"}},
		Links:  map[string]string{"": "https://infisical.example/prod"},
	})
	if writable {
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), strings.Replace(infisicalConfig, `environment: "prod",`, `environment: "prod", write: "missing",`, 1))
	}
	var synced bytes.Buffer
	if err := runEnvSync(context.Background(), clitest.NewDeps(), root, envOptions{}, &synced, &synced); err != nil {
		t.Fatalf("runEnvSync err = %v; out=%s", err, synced.String())
	}
	return root
}

func withVarsUI(t *testing.T, root string, drive func(ctx context.Context, s *varsui.Session)) {
	t.Helper()
	ctx := context.Background()
	err := withEnvProvider(ctx, clitest.NewDeps(), root, envOptions{}, io.Discard, func(runner *provider.Runner, cfg *projectconfig.Config, _ *contractv1.PreflightResponse) error {
		gate, err := discoverVariables(ctx, cfg, runner, envOptions{}, io.Discard)
		if err != nil {
			return err
		}
		s, err := envwire.ServeVarsUI(ctx, cfg, runner, false, gate, nil)
		if err != nil {
			return err
		}
		defer s.Close()
		drive(ctx, s)
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
	for _, row := range state.Matrix.Rows {
		if row.Key == key {
			return row, true
		}
	}
	return envgate.MatrixRow{}, false
}

func TestEnvUIOverAnEnvSource(t *testing.T) {
	t.Run("the page names the source, what it holds, its drift and the credentials it signs in with", func(t *testing.T) {
		root := syncedSourceFixture(t, false)
		withVarsUI(t, root, func(_ context.Context, s *varsui.Session) {
			state := varsUIState(t, s)
			if state.EnvSource == nil || state.EnvSource.ID != "infisical:p-1/prod" || state.EnvSource.Links[""] != "https://infisical.example/prod" {
				t.Fatalf("env source = %+v, want infisical named with its link", state.EnvSource)
			}
			if !slices.Equal(state.EnvSource.Credentials, []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"}) {
				t.Errorf("credentials = %q, want both universal-auth vars", state.EnvSource.Credentials)
			}
			stripe, _ := matrixRow(state, "STRIPE_API_KEY")
			if len(stripe.Cells) == 0 || stripe.Cells[0].EnvSource != "infisical:p-1/prod" {
				t.Errorf("STRIPE_API_KEY = %+v, want its value's source named", stripe)
			}
			credential, declared := matrixRow(state, "INFISICAL_CLIENT_SECRET")
			if !declared || credential.Group != envwire.CredentialGroup || credential.Class != "secret" {
				t.Errorf("INFISICAL_CLIENT_SECRET = %+v (declared %v), want a secret in the %q group", credential, declared, envwire.CredentialGroup)
			}
			if !slices.Contains(state.Matrix.Drift, envgate.Cell{Key: "RETIRED"}) {
				t.Errorf("drift = %+v, want RETIRED, which nothing declares", state.Matrix.Drift)
			}
		})
	})

	t.Run("a value the source lacks is created there when the source may be written", func(t *testing.T) {
		root := syncedSourceFixture(t, true)
		withVarsUI(t, root, func(_ context.Context, s *varsui.Session) {
			res := callVarsUI(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "API_TOKEN", "value": "tok"})
			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(res.Body)
				t.Fatalf("POST = %d: %s", res.StatusCode, body)
			}
			token, _ := matrixRow(varsUIState(t, s), "API_TOKEN")
			if len(token.Cells) == 0 || !token.Cells[0].Set || token.Cells[0].EnvSource != "infisical:p-1/prod" {
				t.Errorf("API_TOKEN = %+v, want it set from the source", token)
			}
		})
		registrations, err := clitest.LoadFakeRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		for _, registration := range registrations {
			if !slices.ContainsFunc(registration.Created, func(held clitest.FakeSourced) bool { return held.Key == "API_TOKEN" && held.Value == "tok" }) {
				t.Errorf("created %+v, want API_TOKEN put into the source", registration.Created)
			}
		}
	})
}
