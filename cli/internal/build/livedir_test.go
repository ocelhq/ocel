package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
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
		builder := nodeOnly{node: func(_ context.Context, _ string, sent []byte, _ Log) error {
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
		builder := nodeOnly{node: func(_ context.Context, _ string, sent []byte, _ Log) error {
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

	t.Run("an app with no encrypted value is handed an empty live dir name, so one inherited from the deployer reaches nothing", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		var got nodeBuildRequest
		builder := nodeOnly{node: requestOf(&got)}
		if err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": {Env: map[string]string{"POSTHOG_ID": "ph-web"}}}, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if dir, ok := got.Apps[0].Env[processenv.LiveDirEnvVar]; !ok || dir != "" {
			t.Errorf("web was handed %s=%q (set %v), want it set empty so the build owns the name", processenv.LiveDirEnvVar, dir, ok)
		}
	})

	t.Run("refuses a key that would land outside the live dir", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		ran := false
		builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
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

	t.Run("names every key the build reads from a file, for the build to drop the deployer's shell copy of it", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		var got nodeBuildRequest
		builder := nodeOnly{node: requestOf(&got)}
		if err := builder.Build(context.Background(), cfg, values, Log{}); err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := []string{"SESSION_SECRET", "STRIPE_API_KEY"}
		if !slices.Equal(got.Apps[0].Unset, want) {
			t.Errorf("Unset = %v, want %v", got.Apps[0].Unset, want)
		}
	})

	t.Run("hides every encrypted value the build says, in its log and in its error", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		var shared, app bytes.Buffer
		log := Log{Shared: &shared, AppLog: func(string) (io.Writer, func(error)) { return &app, func(error) {} }}
		builder := nodeOnly{node: func(_ context.Context, _ string, _ []byte, log Log) error {
			appLog, ended := log.App("web")
			_, _ = io.WriteString(log.Shared, "booting with sk_live_sensitive\n")
			_, _ = io.WriteString(appLog, "signing with ss_live_secret\n")
			ended(nil)
			return errors.New("next build failed: ss_live_secret is not a valid key")
		}}

		err := builder.Build(context.Background(), cfg, values, log)
		if err == nil {
			t.Fatal("Build err = nil, want the node build's failure")
		}
		for name, said := range map[string]string{"shared log": shared.String(), "app log": app.String(), "error": err.Error()} {
			for _, value := range []string{"sk_live_sensitive", "ss_live_secret"} {
				if strings.Contains(said, value) {
					t.Errorf("the %s holds %q: %s", name, value, said)
				}
			}
			if !strings.Contains(said, "[secret]") {
				t.Errorf("the %s = %q, want each value replaced where it was said", name, said)
			}
		}
	})

	t.Run("hides a value the build says across two writes, and passes on what it held once the build ends", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}

		var shared, app bytes.Buffer
		log := Log{Shared: &shared, AppLog: func(string) (io.Writer, func(error)) { return &app, func(error) {} }}
		builder := nodeOnly{node: func(_ context.Context, _ string, _ []byte, log Log) error {
			appLog, ended := log.App("web")
			_, _ = io.WriteString(log.Shared, "booting with sk_live_sen")
			_, _ = io.WriteString(log.Shared, "sitive, ready")
			_, _ = io.WriteString(appLog, "signing with ss_live_")
			_, _ = io.WriteString(appLog, "secret, ready")
			ended(nil)
			return nil
		}}

		if err := builder.Build(context.Background(), cfg, values, log); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if shared.String() != "booting with [secret], ready" {
			t.Errorf("shared log = %q, want the value hidden whole", shared.String())
		}
		if app.String() != "signing with [secret], ready" {
			t.Errorf("app log = %q, want the value hidden whole", app.String())
		}
	})

	t.Run("builds an app whose own build gets no live dir, whatever its keys", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeBuildScript(t, root)
		cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web"), nextContainerApp("api", "apps/api")}}

		builder := nodeOnly{node: requestOf(&nodeBuildRequest{})}
		err := builder.Build(context.Background(), cfg, map[string]AppVariables{"api": {Live: map[string]string{"a/b": "x"}}}, Log{})
		if err != nil {
			t.Errorf("Build err = %v, want the key of an app whose build reads no live dir left alone", err)
		}
	})
}
