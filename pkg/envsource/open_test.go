package envsource_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
)

func execDescriptor(format envsource.Format, command ...string) envsource.Descriptor {
	return envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: command, Format: format}}
}

func noEnv(string) (string, bool) { return "", false }

const printByFolder = `case "$1" in
/) printf '{"ROOT":"r","EMPTY":""}' ;;
/web) printf '{"WEB":"w"}' ;;
esac`

func TestAnExecEnvSourceRunsItsCommandOncePerFolderWithTheFolderSubstituted(t *testing.T) {
	t.Parallel()
	source, err := envsource.Open(execDescriptor(envsource.FormatJSON, "sh", "-c", printByFolder, "sh", envsource.FolderPlaceholder), t.TempDir(), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	read, err := source.Read(context.Background(), []string{"", "/web"})
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if len(read) != 2 || string(read[cell("", "ROOT")].Plaintext) != "r" || string(read[cell("/web", "WEB")].Plaintext) != "w" {
		t.Fatalf("Read() = %v, want ROOT at the root and WEB in /web, and the empty value unset", read)
	}
	if version := read[cell("", "ROOT")].Version; version == "" || version == read[cell("/web", "WEB")].Version {
		t.Errorf("versions = %q and %q, want each named by its value", version, read[cell("/web", "WEB")].Version)
	}
	if source.ID() != "exec" {
		t.Errorf("ID() = %q, want exec", source.ID())
	}
	if err := source.Create(context.Background(), cell("", "NEW"), []byte("v"), ""); !errors.Is(err, envsource.ErrReadOnly) {
		t.Errorf("Create() = %v, want ErrReadOnly", err)
	}
}

func TestAnExecEnvSourceReadsDotenvInTheDirectoryItIsGiven(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "values.env"), []byte("export API_KEY=\"k # kept\"\nOCEL_RESERVED=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := envsource.Open(execDescriptor(envsource.FormatDotenv, "cat", "values.env"), dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	read, err := source.Read(context.Background(), []string{""})
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if len(read) != 1 || string(read[cell("", "API_KEY")].Plaintext) != "k # kept" {
		t.Fatalf("Read() = %v, want API_KEY and never a reserved OCEL_ name", read)
	}
}

func TestAnExecEnvSourceThatFailsNamesItsCommandAndWhatItSaid(t *testing.T) {
	t.Parallel()
	source, err := envsource.Open(execDescriptor(envsource.FormatJSON, "sh", "-c", "echo vault is sealed >&2; exit 3"), t.TempDir(), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Read(context.Background(), []string{""})
	if err == nil || !strings.Contains(err.Error(), "sh") || !strings.Contains(err.Error(), "vault is sealed") {
		t.Fatalf("Read() = %v, want the command and its stderr named", err)
	}
}

func TestAnExecEnvSourceRefusesJSONThatIsNotNamesToText(t *testing.T) {
	t.Parallel()
	for _, printed := range []string{`[1,2]`, `{"PORT":3000}`} {
		source, err := envsource.Open(execDescriptor(envsource.FormatJSON, "printf", printed), t.TempDir(), noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Read(context.Background(), []string{""}); err == nil {
			t.Errorf("Read() of %s = nil, want a refusal", printed)
		}
	}
}

func TestADevInfisicalEnvSourceReadsWithTheTokenInTheShell(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/acme/web", fakeSecret{id: "s1", key: "WEB", value: "w", version: 2})
	fake.set(func(f *fakeInfisical) { f.token = "developer-token" })
	lookupEnv := func(name string) (string, bool) {
		if name == "INFISICAL_TOKEN" {
			return " developer-token\n", true
		}
		return "", false
	}
	descriptor := envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Project: "p-1", Environment: "prod", Path: "/acme", Host: server.URL}}
	source, err := envsource.Open(descriptor, t.TempDir(), lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	read, err := source.Read(context.Background(), []string{"/web"})
	if err != nil || string(read[cell("/web", "WEB")].Plaintext) != "w" {
		t.Fatalf("Read() = %v, %v", read, err)
	}
	if err := source.Create(context.Background(), cell("", "NEW"), []byte("v"), ""); !errors.Is(err, envsource.ErrReadOnly) {
		t.Errorf("Create() from a developer's machine = %v, want ErrReadOnly", err)
	}
}

func TestADevInfisicalEnvSourceWithoutATokenExportsThroughTheInfisicalCLI(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > "$(dirname "$0")/args"
printf '[{"key":"WEB","value":"w","_id":"s1","secretPath":"/acme/web"},{"key":"EMPTY","value":""}]'
`
	if err := os.WriteFile(filepath.Join(bin, "infisical"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	descriptor := envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Project: "p-1", Environment: "dev", Path: "/acme", Host: "https://infisical.example.com/"}}

	source, err := envsource.Open(descriptor, t.TempDir(), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	read, err := source.Read(context.Background(), []string{"/web"})
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if len(read) != 1 || string(read[cell("/web", "WEB")].Plaintext) != "w" {
		t.Fatalf("Read() = %v, want WEB and the empty value unset", read)
	}
	args, err := os.ReadFile(filepath.Join(bin, "args"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"export", "--format=json", "--env=dev", "--path=/acme/web", "--projectId=p-1", "--domain=https://infisical.example.com", "--silent"} {
		if !strings.Contains("\n"+string(args), "\n"+want+"\n") {
			t.Errorf("infisical was run with %q, want %s among its arguments", args, want)
		}
	}
	if source.ID() != "infisical:p-1/dev" {
		t.Errorf("ID() = %q", source.ID())
	}
}

func installInfisicalWithoutFolder(t *testing.T, missing string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--path=` + missing + `" ]; then
    printf 'Request: GET https://infisical.example.com/api/v4/secrets\nResponse Code: 404 Not Found\nMessage: Folder with path %s not found\n' "` + missing + `" >&2
    exit 1
  fi
done
printf '[{"key":"ROOT","value":"r"}]'
`
	if err := os.WriteFile(filepath.Join(bin, "infisical"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestADevInfisicalEnvSourceExportedThroughTheCLIReadsAMissingAppFolderAsEmpty(t *testing.T) {
	installInfisicalWithoutFolder(t, "/acme/web")
	descriptor := envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Project: "p-1", Environment: "dev", Path: "/acme"}}

	source, err := envsource.Open(descriptor, t.TempDir(), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	read, err := source.Read(context.Background(), []string{"", "/web"})
	if err != nil || len(read) != 1 || string(read[cell("", "ROOT")].Plaintext) != "r" {
		t.Fatalf("Read() = %v, %v, want the root's value and nothing for the missing app folder", read, err)
	}
}

func TestADevInfisicalEnvSourceExportedThroughTheCLIFailsOnAMissingRoot(t *testing.T) {
	installInfisicalWithoutFolder(t, "/acme")
	descriptor := envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Project: "p-1", Environment: "dev", Path: "/acme"}}

	source, err := envsource.Open(descriptor, t.TempDir(), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if read, err := source.Read(context.Background(), []string{"", "/web"}); err == nil {
		t.Fatalf("Read() = %v, want a missing root to fail the read", read)
	}
}

func TestADevInfisicalEnvSourceWithNoWayInSaysHowToGetOne(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	descriptor := envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Project: "p-1", Environment: "dev"}}
	_, err := envsource.Open(descriptor, t.TempDir(), noEnv)
	if err == nil || !strings.Contains(err.Error(), "INFISICAL_TOKEN") || !strings.Contains(err.Error(), "infisical login") {
		t.Fatalf("Open() = %v, want the token and the CLI login both offered", err)
	}
}

func TestOnlyACommandOrAServiceIsOpenedAsAnEnvSource(t *testing.T) {
	t.Parallel()
	for _, kind := range []envsource.Kind{envsource.Builtin, envsource.Dotenv} {
		if _, err := envsource.Open(envsource.Descriptor{Kind: kind}, t.TempDir(), noEnv); err == nil {
			t.Errorf("Open(%s) = nil, want a refusal", kind)
		}
	}
}
