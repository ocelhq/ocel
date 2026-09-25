package localsource_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/localsource"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func TestExecRunsItsCommandOncePerFolderWithTheFolderSubstituted(t *testing.T) {
	source := localsource.Exec(envsource.ExecOptions{
		Command: []string{"sh", "-c", `printf '{"FOLDER":"%s","SHARED":"same"}' "$1"`, "exec", "{folder}"},
		Format:  envsource.FormatJSON,
	}, t.TempDir())

	resolved, err := source.Resolve(context.Background(), []string{"", "/web"})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if got := string(resolved[values.Cell{Key: "FOLDER"}].Value); got != "/" {
		t.Errorf("root FOLDER = %q, want the root spelled /", got)
	}
	if got := string(resolved[values.Cell{Folder: "/web", Key: "FOLDER"}].Value); got != "/web" {
		t.Errorf("/web FOLDER = %q", got)
	}
	root, web := resolved[values.Cell{Key: "SHARED"}], resolved[values.Cell{Folder: "/web", Key: "SHARED"}]
	if root.Version == "" || root.Version != web.Version {
		t.Errorf("versions = %q, %q, want one version for one value", root.Version, web.Version)
	}
	if resolved[values.Cell{Key: "FOLDER"}].Version == resolved[values.Cell{Folder: "/web", Key: "FOLDER"}].Version {
		t.Error("two values share a version")
	}
	if source.ID() != "exec" || source.Capabilities().Standing || source.Capabilities().Write {
		t.Errorf("exec = %s %+v, want a read-only source that never stands", source.ID(), source.Capabilities())
	}
}

func TestExecReadsDotenvOutput(t *testing.T) {
	source := localsource.Exec(envsource.ExecOptions{
		Command: []string{"sh", "-c", `printf 'API_KEY=abc\n# note\nexport TOKEN="t w"\n'`},
		Format:  envsource.FormatDotenv,
	}, t.TempDir())
	resolved, err := source.Resolve(context.Background(), []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if string(resolved[values.Cell{Key: "API_KEY"}].Value) != "abc" || string(resolved[values.Cell{Key: "TOKEN"}].Value) != "t w" {
		t.Fatalf("resolved = %v", resolved)
	}
}

func TestExecNamesTheCommandThatFailedAndWhatItSaid(t *testing.T) {
	source := localsource.Exec(envsource.ExecOptions{
		Command: []string{"sh", "-c", `echo "vault is sealed" >&2; exit 3`},
		Format:  envsource.FormatJSON,
	}, t.TempDir())
	_, err := source.Resolve(context.Background(), []string{""})
	if err == nil || !strings.Contains(err.Error(), "vault is sealed") || !strings.Contains(err.Error(), "sh") {
		t.Fatalf("Resolve() of a failing command = %v", err)
	}
}

func TestExecRefusesJSONThatIsNotNamesToStrings(t *testing.T) {
	source := localsource.Exec(envsource.ExecOptions{
		Command: []string{"sh", "-c", `printf '{"PORT":8080}'`},
		Format:  envsource.FormatJSON,
	}, t.TempDir())
	_, err := source.Resolve(context.Background(), []string{""})
	if err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("Resolve() of a number = %v, want PORT named", err)
	}
}

func devOptions(host string) envsource.InfisicalOptions {
	return envsource.InfisicalOptions{Project: "p-1", Environment: "dev", Path: "/acme", Host: host}.Normalized()
}

func noEnv(string) (string, bool) { return "", false }

func TestDevInfisicalReadsWithTheTokenInTheEnvironment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer developer-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v4/secrets" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": []map[string]any{
			{"id": "s1", "secretKey": "API_KEY", "secretValue": "dev-" + r.URL.Query().Get("secretPath"), "version": 1},
		}})
	}))
	t.Cleanup(server.Close)
	env := func(name string) (string, bool) {
		if name == "INFISICAL_TOKEN" {
			return "developer-token", true
		}
		return "", false
	}
	source, err := localsource.DevInfisical(devOptions(server.URL), env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := source.Resolve(context.Background(), []string{"/web"})
	if err != nil || string(resolved[values.Cell{Folder: "/web", Key: "API_KEY"}].Value) != "dev-/acme/web" {
		t.Fatalf("Resolve() = %v, %v", resolved, err)
	}
}

func fakeInfisicalCLI(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "infisical"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDevInfisicalFallsBackToTheCLIYouAreLoggedInTo(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	fakeInfisicalCLI(t, `echo "$@" >> `+record+`
printf '[{"key":"API_KEY","value":"from-cli","secretPath":"/acme/web","_id":"s1"}]'`)
	source, err := localsource.DevInfisical(devOptions("https://infisical.example.com"), noEnv, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := source.Resolve(context.Background(), []string{"/web"})
	if err != nil || string(resolved[values.Cell{Folder: "/web", Key: "API_KEY"}].Value) != "from-cli" {
		t.Fatalf("Resolve() = %v, %v", resolved, err)
	}
	argv, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"export", "--format=json", "--env=dev", "--path=/acme/web", "--projectId=p-1", "--silent", "--domain=https://infisical.example.com"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("argv = %q, want %s", argv, want)
		}
	}
}

func TestDevInfisicalWithNoTokenAndNoCLISaysHowToSignIn(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := localsource.DevInfisical(devOptions(envsource.InfisicalCloud), noEnv, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "INFISICAL_TOKEN") || !strings.Contains(err.Error(), "infisical login") {
		t.Fatalf("DevInfisical() = %v, want both ways in named", err)
	}
}
