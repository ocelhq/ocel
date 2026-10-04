package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func readCodeAndHint(err error) (code, hint string) {
	runError := clierror.NewRunError(fmt.Errorf("deploy: %w", err))
	return runError.GetCode(), runError.GetHint()
}

func installFakeNode(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake node")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}
	t.Setenv("PATH", dir)
}

func TestLoadingWithNoConfigReportsProjectNoConfigWithTheInitCommandAsItsHint(t *testing.T) {
	_, err := Load(context.Background(), t.TempDir(), "")

	code, hint := readCodeAndHint(err)
	if code != "project.no_config" || hint != "ocel init" {
		t.Fatalf("code, hint = %q, %q; want project.no_config, ocel init", code, hint)
	}
}

func TestLoadingAnExplicitConfigPathThatNamesNoConfigReportsProjectNoConfigWithNoHint(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "settings.json"), `{"slug":"acme"}`)
	if err := os.Mkdir(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, path := range map[string]string{
		"a missing file":            "missing.json",
		"a directory":               "configs",
		"a file ocel does not read": "settings.json",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(context.Background(), dir, path)

			code, hint := readCodeAndHint(err)
			if code != "project.no_config" || hint != "" {
				t.Fatalf("code, hint = %q, %q; want project.no_config and no hint (err %v)", code, hint, err)
			}
		})
	}
}

func TestALoadedConfigThatFailsReportsProjectInvalidConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		file, source, other, hint string
	}{
		"malformed JSON":       {DefaultFileName, "{", "", ""},
		"malformed YAML":       {YAMLFileName, "slug: [\n", "", ""},
		"a missing slug":       {DefaultFileName, `{"provider":{"fake":{}}}`, "", "slug"},
		"an invalid slug":      {DefaultFileName, `{"slug":"Not A Slug"}`, "", "slug"},
		"an unknown key":       {DefaultFileName, `{"slug":"acme","bogus":1}`, "", "bogus"},
		"two config forms":     {DefaultFileName, `{"slug":"acme"}`, YAMLFileName, ""},
		"a bad app name":       {DefaultFileName, `{"slug":"acme","apps":[{"name":"A_B","path":"."}]}`, "", "apps[0].name"},
		"a reserved app name":  {DefaultFileName, `{"slug":"acme","apps":[{"name":"` + naming.InfraApp + `","path":"."}]}`, "", "apps[0].name"},
		"a duplicate app name": {DefaultFileName, `{"slug":"acme","apps":[{"name":"web","path":"."},{"name":"web","path":"b"}]}`, "", "apps[1].name"},
		"a missing app path":   {DefaultFileName, `{"slug":"acme","apps":[{"name":"web"}]}`, "", "apps[0].path"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, tc.file), tc.source)
			if tc.other != "" {
				write(t, filepath.Join(dir, tc.other), "slug: acme\n")
			}

			_, err := Load(context.Background(), dir, "")

			code, hint := readCodeAndHint(err)
			if code != "project.invalid_config" || hint != tc.hint {
				t.Fatalf("code, hint = %q, %q; want project.invalid_config, %q (err %v)", code, hint, tc.hint, err)
			}
		})
	}
}

func TestATypeScriptConfigThatFailsToEvaluateReportsProjectInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `export default { slug: "acme" };`)
	installFakeNode(t, "echo 'BuildEnvError: DATABASE_URL is not set' >&2\nexit 1")

	_, err := Load(context.Background(), dir, "")

	if code, _ := readCodeAndHint(err); code != "project.invalid_config" {
		t.Fatalf("code = %q, want project.invalid_config (err %v)", code, err)
	}
}

func TestAConfigThatFailsForAReasonOutsideItReportsInternal(t *testing.T) {
	t.Run("an unreadable .env", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
		if err := os.Mkdir(filepath.Join(dir, ".env"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		_, err := Load(context.Background(), dir, "")

		if code, _ := readCodeAndHint(err); err == nil || code != "internal" {
			t.Fatalf("code = %q, want internal (err %v)", code, err)
		}
	})
	t.Run("a state directory that cannot be created", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, `export default { slug: "acme" };`)
		write(t, filepath.Join(dir, statedir.Name), "")

		_, err := Load(context.Background(), dir, "")

		if code, _ := readCodeAndHint(err); err == nil || code != "internal" {
			t.Fatalf("code = %q, want internal (err %v)", code, err)
		}
	})
	t.Run("node missing from PATH", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, `export default { slug: "acme" };`)
		t.Setenv("PATH", t.TempDir())

		_, err := Load(context.Background(), dir, "")

		if code, _ := readCodeAndHint(err); err == nil || code != "internal" {
			t.Fatalf("code = %q, want internal (err %v)", code, err)
		}
	})
}

func TestAProjectWithNoProviderReportsProjectInvalidConfigHintingProvider(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
	loaded, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err = loaded.RequireProvider()

	code, hint := readCodeAndHint(err)
	if code != "project.invalid_config" || hint != "provider" {
		t.Fatalf("code, hint = %q, %q; want project.invalid_config, provider", code, hint)
	}
}

func TestACodedLoadFailureReadsAsItsCauseDid(t *testing.T) {
	_, err := Load(context.Background(), t.TempDir(), "")

	var missing NoConfigError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want it to wrap a NoConfigError", err)
	}
	if err.Error() != missing.Error() {
		t.Fatalf("err = %q, want %q", err, missing.Error())
	}
}
