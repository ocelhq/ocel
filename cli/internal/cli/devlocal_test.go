package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/resolve"
)

const localLink = `{"name":"main","postgres":{"host":"localhost","port":5432,"database":"app","username":"app","password":"pw"}}`

func localDeps(t *testing.T) cmddeps.Deps {
	t.Helper()
	deps := newDeps()
	deps.LoadCredentials = func() (credentials.Credentials, error) {
		t.Error("a local run read the credentials; it must never need a login")
		return credentials.Credentials{}, errors.New("no credentials")
	}
	deps.FetchAccount = func(context.Context, string, string, string) (resolve.Account, error) {
		t.Error("a local run fetched the account; it must never reach the console")
		return resolve.Account{}, errors.New("no console")
	}
	return deps
}

func TestRunDevLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	t.Run("an unlinked project resolves its resource from the dotfile and spawns", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "infra", "main.ts"), declareResourceScript("main"))
		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "OCEL_RESOURCE_POSTGRES_main="+localLink+"\n")

		envDumpPath := filepath.Join(root, "env.out")
		appCmd := []string{"sh", "-c", "env > " + envDumpPath + "; exit 7"}

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), localDeps(t), true, root, appCmd, &stdout, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != 7 {
			t.Fatalf("runDev err = %v, want exit 7; stderr=%s", err, stderr.String())
		}

		dumped, readErr := os.ReadFile(envDumpPath)
		if readErr != nil {
			t.Fatalf("read env dump: %v", readErr)
		}
		env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
		if got := env["OCEL_RESOURCE_POSTGRES_main"]; got != localLink {
			t.Fatalf("app env OCEL_RESOURCE_POSTGRES_main = %q, want the dotfile's own %q", got, localLink)
		}
		if _, ok := env["OCEL_RUNTIME_ADDRESS"]; !ok {
			t.Error("the app was told no runtime address, so it can declare nothing")
		}
	})

	t.Run("a declared resource with no entry refuses, naming the entry and an example", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "infra", "main.ts"), declareResourceScript("main"))

		var stdout, stderr syncBuffer
		err := runDev(context.Background(), localDeps(t), true, root, []string{"sh", "-c", "exit 0"}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDev started the app with no value for the resource it declared")
		}
		for _, want := range []string{
			"OCEL_RESOURCE_POSTGRES_main",
			`"postgres"`,
			dotenv.FileName,
			"ocel dev --local",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal = %q, want it to name %q", err.Error(), want)
			}
		}
	})
}

func TestRunRunLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture command")
	}

	t.Run("a one-off command carries the dotfile's resources with no console", func(t *testing.T) {
		root := t.TempDir()
		t.Cleanup(func() { _ = lockfile.Remove(root) })

		clitest.WriteFile(t, filepath.Join(root, "infra", "main.ts"), declareResourceScript("main"))
		clitest.WriteFile(t, filepath.Join(root, dotenv.FileName), "OCEL_RESOURCE_POSTGRES_main="+localLink+"\n")

		envDumpPath := filepath.Join(root, "env.out")
		var stdout, stderr syncBuffer
		err := runRun(context.Background(), localDeps(t), true, root, []string{"sh", "-c", "env > " + envDumpPath}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runRun err = %v; stderr=%s", err, stderr.String())
		}

		dumped, readErr := os.ReadFile(envDumpPath)
		if readErr != nil {
			t.Fatalf("read env dump: %v", readErr)
		}
		env := toMap(strings.Split(strings.TrimRight(string(dumped), "\n"), "\n"))
		if got := env["OCEL_RESOURCE_POSTGRES_main"]; got != localLink {
			t.Fatalf("command env OCEL_RESOURCE_POSTGRES_main = %q, want the dotfile's own %q", got, localLink)
		}
	})
}
