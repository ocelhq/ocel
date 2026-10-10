package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/variables/baked"
)

func sealedTaskRoot(t *testing.T, key []byte, values map[string]string) string {
	t.Helper()
	root := t.TempDir()
	sealed, err := baked.Seal(key, values)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	path := filepath.Join(root, baked.FilePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func dataKey() []byte { return bytes.Repeat([]byte{9}, baked.KeyBytes) }

func envelope(key []byte) string { return base64.StdEncoding.EncodeToString(key) }

func TestBakedVariablesAreInjectedUnderTheirOwnNames(t *testing.T) {
	t.Run("injects each value under the name it was declared under", func(t *testing.T) {
		root := sealedTaskRoot(t, dataKey(), map[string]string{"STRIPE_API_KEY": "sk-live", "WEBHOOK_SECRET": "whsec"})

		env, err := bakedVariablesEnv(envelope(dataKey()), root)
		if err != nil {
			t.Fatalf("bakedVariablesEnv: %v", err)
		}

		want := []string{"STRIPE_API_KEY=sk-live", "WEBHOOK_SECRET=whsec"}
		if !slices.Equal(env, want) {
			t.Fatalf("env = %v, want %v", env, want)
		}
	})

	t.Run("a sealed value wins over the same name in the function's configuration", func(t *testing.T) {
		t.Setenv("STRIPE_API_KEY", "from-the-function-configuration")
		root := sealedTaskRoot(t, dataKey(), map[string]string{"STRIPE_API_KEY": "sk-live"})
		sealed, err := bakedVariablesEnv(envelope(dataKey()), root)
		if err != nil {
			t.Fatalf("bakedVariablesEnv: %v", err)
		}
		extra := childEnv(sealed, nil, nil)

		for name, env := range map[string][]string{
			"node":       nodeChildEnv("/tmp/ocel-control.sock", extra),
			"executable": executableEnv(4321, extra),
		} {
			if got := lastValue(env, "STRIPE_API_KEY"); got != "sk-live" {
				t.Errorf("%s child reads STRIPE_API_KEY = %q, want the sealed value", name, got)
			}
		}
	})

	t.Run("no envelope is no work at all", func(t *testing.T) {
		env, err := bakedVariablesEnv("", sealedTaskRoot(t, dataKey(), map[string]string{"A": "one"}))
		if err != nil {
			t.Fatalf("bakedVariablesEnv: %v", err)
		}
		if env != nil {
			t.Errorf("env = %v, want nothing", env)
		}
	})

	t.Run("every failure is diagnosable", func(t *testing.T) {
		sealed := sealedTaskRoot(t, dataKey(), map[string]string{"A": "one"})

		cases := []struct {
			name    string
			env     string
			root    string
			wantAny []string
		}{
			{
				name:    "the package contains no sealed file",
				env:     envelope(dataKey()),
				root:    t.TempDir(),
				wantAny: []string{baked.FilePath},
			},
			{
				name:    "the envelope is not an envelope",
				env:     "not base64!",
				root:    sealed,
				wantAny: []string{baked.EnvelopeVar},
			},
			{
				name:    "the envelope is the wrong size for a data key",
				env:     envelope([]byte("short")),
				root:    sealed,
				wantAny: []string{"data key"},
			},
			{
				name:    "the key is not the one the bundle was sealed under",
				env:     envelope(bytes.Repeat([]byte{1}, baked.KeyBytes)),
				root:    sealed,
				wantAny: []string{"decrypt baked variables"},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				env, err := bakedVariablesEnv(tc.env, tc.root)
				if err == nil {
					t.Fatalf("bakedVariablesEnv = %v, want a failed init", env)
				}
				if env != nil {
					t.Errorf("env = %v, want nothing alongside the error", env)
				}
				for _, want := range tc.wantAny {
					if !bytes.Contains([]byte(err.Error()), []byte(want)) {
						t.Errorf("error does not name %q: %v", want, err)
					}
				}
			})
		}
	})
}

func TestBakedVariablesOpenWithNoCredentialsAndNoEndpoint(t *testing.T) {
	t.Run("opens with no credentials and no endpoint", func(t *testing.T) {
		root := sealedTaskRoot(t, dataKey(), map[string]string{"STRIPE_API_KEY": "sk-live"})
		for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE"} {
			t.Setenv(key, "")
		}
		t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
		t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
		t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
		t.Setenv("LAMBDA_TASK_ROOT", root)
		t.Setenv(baked.EnvelopeVar, envelope(dataKey()))

		env, err := resolveBakedVariablesEnv()
		if err != nil {
			t.Fatalf("resolveBakedVariablesEnv: %v", err)
		}
		if want := []string{"STRIPE_API_KEY=sk-live"}; !slices.Equal(env, want) {
			t.Fatalf("env = %v, want %v", env, want)
		}
	})
}

func lastValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if name, v, ok := strings.Cut(entry, "="); ok && name == key {
			value = v
		}
	}
	return value
}
