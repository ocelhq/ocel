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

	t.Run("every app builds with the id its own deploy will send", func(t *testing.T) {
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
		if err := builder.Build(context.Background(), cfg, envByApp, "", io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}

		seen := map[string]string{}
		for _, app := range gotReq.Apps {
			recorded, err := DeploymentID(root, app.Name)
			if err != nil {
				t.Fatalf("DeploymentID(%s): %v", app.Name, err)
			}
			if got := app.Env[deploymentIDEnv]; got != recorded {
				t.Errorf("app %s built with %s = %q, want its recorded %q", app.Name, deploymentIDEnv, got, recorded)
			}
			if other, taken := seen[recorded]; taken {
				t.Errorf("apps %s and %s both deploy as %q, want an id each", other, app.Name, recorded)
			}
			seen[recorded] = app.Name
		}
		if got := gotReq.Apps[0].Env["POSTHOG_ID"]; got != "ph-123" {
			t.Errorf("app web lost its resolved value: POSTHOG_ID = %q", got)
		}
		if got, taken := lookup(gotEnv, deploymentIDEnv); taken {
			t.Errorf("builder env carries %s = %q, want each app to carry its own", deploymentIDEnv, got)
		}
	})

	t.Run("an app the builder names itself is recorded under that name", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{Dir: root}
		var gotEnv []string
		builder := Builder{Exec: func(_ context.Context, _ string, env []string, request []byte, _ io.Writer) error {
			gotEnv = env
			var req builderRequest
			if err := json.Unmarshal(request, &req); err != nil {
				return err
			}
			if len(req.Apps) != 0 {
				t.Errorf("request declares %d apps, want the builder to detect one", len(req.Apps))
			}
			id, _ := lookup(env, deploymentIDEnv)
			if err := os.MkdirAll(filepath.Join(req.OutDir, appsDirName, "detected"), 0o755); err != nil {
				return err
			}
			if id == "" {
				t.Errorf("builder env carries no %s for the app it detects", deploymentIDEnv)
			}
			writePlan(t, req.OutDir)
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, nil, "", io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}
		recorded, err := DeploymentID(root, "detected")
		if err != nil {
			t.Fatalf("DeploymentID: %v", err)
		}
		if built, _ := lookup(gotEnv, deploymentIDEnv); built != recorded {
			t.Errorf("detected app built under %q, recorded %q", built, recorded)
		}
	})

	t.Run("a fresh build supersedes the id the last one recorded", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuilder(t, root)
		cfg := &projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}}}
		builder := Builder{Exec: func(_ context.Context, _ string, _ []string, request []byte, _ io.Writer) error {
			var req builderRequest
			if err := json.Unmarshal(request, &req); err != nil {
				return err
			}
			writePlan(t, req.OutDir)
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, nil, "", io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}
		first, err := DeploymentID(root, "web")
		if err != nil {
			t.Fatalf("DeploymentID: %v", err)
		}
		if err := builder.Build(context.Background(), cfg, nil, "", io.Discard); err != nil {
			t.Fatalf("Build: %v", err)
		}
		second, err := DeploymentID(root, "web")
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
		err := Build(context.Background(), cfg, map[string]map[string]string{"": {deploymentIDEnv: "mine"}}, "", io.Discard)
		if err == nil || !strings.Contains(err.Error(), deploymentIDEnv) {
			t.Errorf("Build err = %v, want it to refuse a variable named %s", err, deploymentIDEnv)
		}
	})
}

func TestDeploymentID(t *testing.T) {
	t.Parallel()

	t.Run("an app carrying no id points at ocel build", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, scratchDirName, outputDirName, appsDirName, "web"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeDeploymentID(root, "web", "d1a2b3c4d5e6f708192a3b4c5d6e7f80"); err != nil {
			t.Fatal(err)
		}
		_, err := DeploymentID(root, "admin")
		if err == nil || !strings.Contains(err.Error(), "ocel build") {
			t.Errorf("DeploymentID err = %v, want it to point at `ocel build`", err)
		}
		if err == nil || !strings.Contains(err.Error(), "admin") {
			t.Errorf("DeploymentID err = %v, want it to name the app", err)
		}
	})

	t.Run("an id that is not what a build mints points at ocel build", func(t *testing.T) {
		t.Parallel()

		for _, recorded := range []string{
			"",
			"   ",
			"dep1",
			"D1A2B3C4D5E6F708192A3B4C5D6E7F80",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f8",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f800",
			"../../etc/passwd",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f80 extra",
		} {
			root := t.TempDir()
			if err := writeDeploymentID(root, "web", recorded); err != nil {
				t.Fatal(err)
			}
			_, err := DeploymentID(root, "web")
			if err == nil || !strings.Contains(err.Error(), "ocel build") {
				t.Errorf("DeploymentID with %q recorded: err = %v, want it to point at `ocel build`", recorded, err)
			}
		}
	})

	t.Run("reads back the id a build minted", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		minted, err := mintDeploymentID()
		if err != nil {
			t.Fatal(err)
		}
		if err := writeDeploymentID(root, "web", minted); err != nil {
			t.Fatal(err)
		}
		got, err := DeploymentID(root, "web")
		if err != nil {
			t.Fatalf("DeploymentID: %v", err)
		}
		if got != minted {
			t.Errorf("DeploymentID = %q, want %q", got, minted)
		}
	})
}
