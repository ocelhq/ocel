package appbuilder

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func builtWithPhase(t *testing.T, cfg *projectconfig.Config, envByApp map[string]map[string]string, phase string) (builderRequest, []string) {
	t.Helper()

	var gotReq builderRequest
	var gotEnv []string
	builder := Builder{Exec: func(_ context.Context, _ string, env []string, request []byte, _ io.Writer) error {
		gotEnv = env
		if err := json.Unmarshal(request, &gotReq); err != nil {
			return err
		}
		writePlan(t, gotReq.OutDir)
		return nil
	}}
	if err := builder.Build(context.Background(), cfg, envByApp, phase, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return gotReq, gotEnv
}

func TestBuildHandsTheAppTheSuppressedPhase(t *testing.T) {
	t.Parallel()

	t.Run("a suppressed deploy builds every app in the phase", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{
			Dir: root,
			Apps: []projectconfig.App{
				{Name: "web", Path: "apps/web", Framework: "next"},
				{Name: "docs", Path: "apps/docs", Framework: "next"},
			},
		}

		req, _ := builtWithPhase(t, cfg, map[string]map[string]string{"web": {"POSTHOG_ID": "ph-123"}}, providerkit.PhaseResourcesSuppressed)

		for _, app := range req.Apps {
			if got := app.Env[providerkit.PhaseEnvName]; got != providerkit.PhaseResourcesSuppressed {
				t.Errorf("app %s built with %s = %q, want %q, so the SDK knows nothing is provisioned", app.Name, providerkit.PhaseEnvName, got, providerkit.PhaseResourcesSuppressed)
			}
		}
		if got := req.Apps[0].Env["POSTHOG_ID"]; got != "ph-123" {
			t.Errorf("app web lost its resolved value: POSTHOG_ID = %q", got)
		}
	})

	t.Run("an app the builder detects itself is built in the phase too", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)

		_, env := builtWithPhase(t, &projectconfig.Config{Dir: root}, nil, providerkit.PhaseResourcesSuppressed)

		if got, _ := lookup(env, providerkit.PhaseEnvName); got != providerkit.PhaseResourcesSuppressed {
			t.Errorf("builder env carries %s = %q, want %q", providerkit.PhaseEnvName, got, providerkit.PhaseResourcesSuppressed)
		}
	})

	t.Run("a deploy that provisions says nothing about a phase", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}}}

		req, env := builtWithPhase(t, cfg, nil, "")

		if got, taken := req.Apps[0].Env[providerkit.PhaseEnvName]; taken {
			t.Errorf("app web built with %s = %q, want no phase where everything is provisioned", providerkit.PhaseEnvName, got)
		}
		if got, taken := lookup(env, providerkit.PhaseEnvName); taken {
			t.Errorf("builder env carries %s = %q, want no phase where everything is provisioned", providerkit.PhaseEnvName, got)
		}
	})

	t.Run("a variable declared as the phase is refused", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}}}

		err := Build(context.Background(), cfg, map[string]map[string]string{"web": {providerkit.PhaseEnvName: "mine"}}, "", io.Discard)
		if err == nil || !strings.Contains(err.Error(), providerkit.PhaseEnvName) {
			t.Errorf("Build err = %v, want it to refuse a variable named %s", err, providerkit.PhaseEnvName)
		}
	})
}
