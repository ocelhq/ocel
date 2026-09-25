package env

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

const infisicalConfig = `
export default {
  slug: "` + clitest.FixtureSlug + `",
  provider: { aws: {} },
  domains: { preview: "*.preview.acme.com" },
  envSource: {
    production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { var: "INFISICAL_CLIENT_ID" }, clientSecret: { var: "INFISICAL_CLIENT_SECRET" } } } } },
    preview: { exec: { command: ["sh", "-c", "printf 'LOG_LEVEL=debug'"], format: "dotenv" } },
  },
};
`

func setUpSourcedFixture(t *testing.T, source clitest.FakeEnvSource) string {
	t.Helper()
	root := setUpEnvFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalConfig)
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(clitest.FakeEnvSourceEnvVar, string(raw))
	return root
}

func TestEnvSyncReadsTheTierSourceNow(t *testing.T) {
	root := setUpSourcedFixture(t, clitest.FakeEnvSource{
		ID:      "infisical:p-1/prod",
		Values:  []clitest.FakeSourced{{Key: "STRIPE_API_KEY", Value: "sk"}, {Key: "API_TOKEN", Value: "t"}},
		Links:   map[string]string{"": "https://infisical.example/prod"},
		Attempt: 1_700_000_000,
		Success: 1_700_000_000,
	})

	var stdout, stderr bytes.Buffer
	if err := runEnvSync(context.Background(), clitest.NewDeps(), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSync err = %v; stderr=%s", err, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "infisical:p-1/prod") || !strings.Contains(out, "2 written") {
		t.Errorf("stdout = %q, want the source named and both values written", out)
	}

	var ls bytes.Buffer
	if err := runEnvLs(context.Background(), clitest.NewDeps(), root, envOptions{}, &ls, &ls); err != nil {
		t.Fatalf("runEnvLs err = %v; out=%s", err, ls.String())
	}
	for _, line := range strings.Split(ls.String(), "\n") {
		if strings.HasPrefix(line, "STRIPE_API_KEY") && !strings.HasSuffix(line, "infisical:p-1/prod") {
			t.Errorf("ls line = %q, want the value's source in the SOURCE column", line)
		}
	}

	t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
	var preview bytes.Buffer
	if err := runEnvSync(context.Background(), clitest.NewDeps(), root, envOptions{preview: true}, &preview, &preview); err != nil {
		t.Fatalf("runEnvSync --preview err = %v; out=%s", err, preview.String())
	}
	if out := preview.String(); !strings.Contains(out, "exec") || !strings.Contains(out, "1 written") {
		t.Errorf("stdout = %q, want exec's output written", out)
	}
}

func TestEnvSourceDescribesWhereATierReadsFrom(t *testing.T) {
	root := setUpSourcedFixture(t, clitest.FakeEnvSource{
		ID:       "infisical:p-1/prod",
		Links:    map[string]string{"": "https://infisical.example/prod"},
		Attempt:  1_700_000_120,
		Success:  1_700_000_000,
		LastSeen: "Infisical answered 503",
	})

	var before bytes.Buffer
	if err := runEnvSource(context.Background(), clitest.NewDeps(), root, envOptions{}, &before, &before); err != nil {
		t.Fatalf("runEnvSource err = %v; out=%s", err, before.String())
	}
	if out := before.String(); !strings.Contains(out, "builtin") || !strings.Contains(out, "infisical:p-1/prod") {
		t.Errorf("stdout = %q, want the target's builtin store named beside the configured source a sync would bring in", out)
	}

	var synced bytes.Buffer
	if err := runEnvSync(context.Background(), clitest.NewDeps(), root, envOptions{}, &synced, &synced); err != nil {
		t.Fatalf("runEnvSync err = %v; out=%s", err, synced.String())
	}
	var stdout, stderr bytes.Buffer
	if err := runEnvSource(context.Background(), clitest.NewDeps(), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSource err = %v; stderr=%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"infisical:p-1/prod", "standing", "yes", "Infisical answered 503", "https://infisical.example/prod", "INFISICAL_CLIENT_SECRET"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
}

func TestEnvSetOnAValueTheSourceOwnsSaysWhereToChangeIt(t *testing.T) {
	root := setUpSourcedFixture(t, clitest.FakeEnvSource{ID: "infisical:p-1/prod", Values: []clitest.FakeSourced{{Key: "STRIPE_API_KEY", Value: "sk"}}})
	var synced bytes.Buffer
	if err := runEnvSync(context.Background(), clitest.NewDeps(), root, envOptions{}, &synced, &synced); err != nil {
		t.Fatalf("runEnvSync err = %v; out=%s", err, synced.String())
	}

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), clitest.NewDeps(), root, "STRIPE_API_KEY", "by-hand", envOptions{}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "infisical:p-1/prod") {
		t.Fatalf("runEnvSet err = %v, want the source that owns the value named", err)
	}
	envSet(t, root, "INFISICAL_CLIENT_SECRET", "secret", envOptions{})
}
