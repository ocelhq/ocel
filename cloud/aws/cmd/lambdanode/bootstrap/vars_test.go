package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cloud/aws/vars/baked"
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

func TestBakedVarsEnv_InjectsUnderTheNamespacedNameOnly(t *testing.T) {
	root := sealedTaskRoot(t, dataKey(), map[string]string{"STRIPE_API_KEY": "sk-live", "WEBHOOK_SECRET": "whsec"})

	env, err := bakedVarsEnv(envelope(dataKey()), root)
	if err != nil {
		t.Fatalf("bakedVarsEnv: %v", err)
	}

	want := []string{"OCEL_VAR_STRIPE_API_KEY=sk-live", "OCEL_VAR_WEBHOOK_SECRET=whsec"}
	if !slices.Equal(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
	for _, entry := range env {
		if len(entry) > 0 && entry[0] != 'O' {
			t.Errorf("entry %q is not namespaced", entry)
		}
	}
}

func TestResolveBakedVarsEnv_OpensWithNoCredentialsAndNoEndpoint(t *testing.T) {
	root := sealedTaskRoot(t, dataKey(), map[string]string{"STRIPE_API_KEY": "sk-live"})
	for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE"} {
		t.Setenv(key, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("LAMBDA_TASK_ROOT", root)
	t.Setenv(baked.EnvelopeVar, envelope(dataKey()))

	env, err := resolveBakedVarsEnv()
	if err != nil {
		t.Fatalf("resolveBakedVarsEnv: %v", err)
	}
	if want := []string{"OCEL_VAR_STRIPE_API_KEY=sk-live"}; !slices.Equal(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestBakedVarsEnv_NoEnvelopeIsNoWorkAtAll(t *testing.T) {
	env, err := bakedVarsEnv("", sealedTaskRoot(t, dataKey(), map[string]string{"A": "one"}))
	if err != nil {
		t.Fatalf("bakedVarsEnv: %v", err)
	}
	if env != nil {
		t.Errorf("env = %v, want nothing", env)
	}
}

func TestBakedVarsEnv_EveryFailureIsDiagnosable(t *testing.T) {
	sealed := sealedTaskRoot(t, dataKey(), map[string]string{"A": "one"})

	cases := []struct {
		name    string
		env     string
		root    string
		wantAny []string
	}{
		{
			name:    "the package carries no sealed file",
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
			env, err := bakedVarsEnv(tc.env, tc.root)
			if err == nil {
				t.Fatalf("bakedVarsEnv = %v, want a failed init", env)
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
}
