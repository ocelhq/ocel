package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/processenv"
)

func TestABuildReadsItsEncryptedValuesFromALiveDirAndNeverFromItsEnvironment(t *testing.T) {
	values := map[string]AppVariables{"web": {
		Env:  map[string]string{"POSTHOG_ID": "ph-web"},
		Live: map[string]string{"STRIPE_API_KEY": "sk_live_sensitive", "SESSION_SECRET": "ss_live_secret"},
	}}

	t.Run("hands the build each value as a file in a private directory outside the project", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		var request []byte
		var liveDir string
		files := map[string]string{}
		modes := map[string]os.FileMode{}
		builder := nodeOnly{host: servingNext, node: func(_ context.Context, _ string, sent []byte, _ Log) error {
			request = sent
			var got nodeBuildRequest
			if err := json.Unmarshal(sent, &got); err != nil {
				return err
			}
			liveDir = got.Apps[0].Env[processenv.LiveDirEnvVar]
			if liveDir == "" {
				return errors.New("the build was handed no live dir")
			}
			info, err := os.Stat(liveDir)
			if err != nil {
				return err
			}
			modes[liveDir] = info.Mode().Perm()
			for _, key := range []string{"STRIPE_API_KEY", "SESSION_SECRET"} {
				path := filepath.Join(liveDir, key)
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				info, err := os.Stat(path)
				if err != nil {
					return err
				}
				files[key] = string(body)
				modes[key] = info.Mode().Perm()
			}
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, values, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}

		if files["STRIPE_API_KEY"] != "sk_live_sensitive" || files["SESSION_SECRET"] != "ss_live_secret" {
			t.Errorf("live dir held %v, want each encrypted value under its key", files)
		}
		if modes[liveDir] != 0o700 {
			t.Errorf("live dir mode = %v, want 0700", modes[liveDir])
		}
		for _, key := range []string{"STRIPE_API_KEY", "SESSION_SECRET"} {
			if modes[key] != 0o600 {
				t.Errorf("%s mode = %v, want 0600", key, modes[key])
			}
		}
		if within, _ := filepath.Rel(root, liveDir); !strings.HasPrefix(within, "..") {
			t.Errorf("live dir %s is inside the project %s, want it where no build output or upload reaches", liveDir, root)
		}
		for _, value := range []string{"sk_live_sensitive", "ss_live_secret"} {
			if bytes.Contains(request, []byte(value)) {
				t.Errorf("the build request holds %q, want encrypted values only in the live dir", value)
			}
		}
		if !bytes.Contains(request, []byte("ph-web")) {
			t.Error("the build request lacks the plaintext POSTHOG_ID, want plaintext values in the env as before")
		}
		if _, err := os.Stat(liveDir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("live dir after the build: stat err = %v, want it removed", err)
		}
	})

	t.Run("removes the live dir when the build fails", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		var liveDir string
		builder := nodeOnly{host: servingNext, node: func(_ context.Context, _ string, sent []byte, _ Log) error {
			var got nodeBuildRequest
			if err := json.Unmarshal(sent, &got); err != nil {
				return err
			}
			liveDir = got.Apps[0].Env[processenv.LiveDirEnvVar]
			return errors.New("next build failed")
		}}

		if err := builder.Build(context.Background(), cfg, values, Log{}); err == nil {
			t.Fatal("Build err = nil, want the node build's failure")
		}
		if liveDir == "" {
			t.Fatal("the build was handed no live dir")
		}
		if _, err := os.Stat(liveDir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("live dir after a failed build: stat err = %v, want it removed", err)
		}
	})

	t.Run("an app with no encrypted value is handed no live dir", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		var got nodeBuildRequest
		builder := nodeOnly{host: servingNext, node: requestOf(&got)}
		if err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": {Env: map[string]string{"POSTHOG_ID": "ph-web"}}}, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if dir, ok := got.Apps[0].Env[processenv.LiveDirEnvVar]; ok {
			t.Errorf("web was handed %s=%q, want none for an app with nothing to put there", processenv.LiveDirEnvVar, dir)
		}
	})

	t.Run("refuses a key that would land outside the live dir", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		ran := false
		builder := nodeOnly{host: servingNext, node: func(context.Context, string, []byte, Log) error {
			ran = true
			return nil
		}}
		err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": {Live: map[string]string{"../ESCAPED": "x"}}}, Log{})
		if err == nil || !strings.Contains(err.Error(), "../ESCAPED") {
			t.Errorf("Build err = %v, want a refusal naming the key", err)
		}
		if ran {
			t.Error("the node build script ran, want the refusal before anything is built")
		}
	})
}
