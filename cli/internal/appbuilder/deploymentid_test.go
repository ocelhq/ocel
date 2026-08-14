package appbuilder

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestBuildStampsTheDeploymentID(t *testing.T) {
	t.Parallel()

	t.Run("every app builds with the id the deploy will promote under", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{
			Dir: root,
			Apps: []projectconfig.App{
				{Name: "web", Path: "apps/web", Framework: "next", Compute: "serverless"},
				{Name: "docs", Path: "apps/docs", Framework: "next", Compute: "serverless"},
			},
		}

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

		envByApp := map[string]map[string]string{"web": {"POSTHOG_ID": "ph-123"}}
		if err := builder.Build(context.Background(), cfg, envByApp, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}

		recorded, err := DeploymentID(root)
		if err != nil {
			t.Fatalf("DeploymentID: %v", err)
		}
		if len(recorded) != 32 {
			t.Errorf("recorded id = %q, want 32 hex characters", recorded)
		}
		for _, app := range gotReq.Apps {
			if got := app.Env[deploymentIDEnv]; got != recorded {
				t.Errorf("app %s built with %s = %q, want the recorded %q", app.Name, deploymentIDEnv, got, recorded)
			}
		}
		if got := gotReq.Apps[0].Env["POSTHOG_ID"]; got != "ph-123" {
			t.Errorf("app web lost its resolved value: POSTHOG_ID = %q", got)
		}
		if got, _ := lookup(gotEnv, deploymentIDEnv); got != recorded {
			t.Errorf("builder env %s = %q, want the recorded %q", deploymentIDEnv, got, recorded)
		}
	})

	t.Run("a fresh build supersedes the id the last one recorded", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{Dir: root}
		builder := Builder{Exec: func(_ context.Context, _ string, _ []string, request []byte, _ io.Writer) error {
			var req builderRequest
			if err := json.Unmarshal(request, &req); err != nil {
				return err
			}
			writePlan(t, req.OutDir)
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, nil, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}
		first, err := DeploymentID(root)
		if err != nil {
			t.Fatalf("DeploymentID: %v", err)
		}
		if err := builder.Build(context.Background(), cfg, nil, io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}
		second, err := DeploymentID(root)
		if err != nil {
			t.Fatalf("DeploymentID: %v", err)
		}
		if first == second {
			t.Errorf("both builds recorded %q, want each build its own id", first)
		}
	})

	t.Run("a variable may not claim the name the build owns", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		cfg := &projectconfig.Config{Dir: root}
		err := Build(context.Background(), cfg, map[string]map[string]string{"": {deploymentIDEnv: "mine"}}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), deploymentIDEnv) {
			t.Errorf("Build err = %v, want it to refuse a variable named %s", err, deploymentIDEnv)
		}
	})
}

func TestDeploymentID(t *testing.T) {
	t.Parallel()

	t.Run("output that predates the id points at ocel build", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, scratchDirName, outputDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := DeploymentID(root)
		if err == nil || !strings.Contains(err.Error(), "ocel build") {
			t.Errorf("DeploymentID err = %v, want it to point at `ocel build`", err)
		}
	})
}
