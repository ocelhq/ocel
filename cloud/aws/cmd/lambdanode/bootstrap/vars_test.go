package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/cloud/aws/vars/baked"
)

// fakeUnwrapper stands in for KMS: it returns the data key the deploy sealed
// with, and records that it was asked at all.
type fakeUnwrapper struct {
	key   []byte
	err   error
	calls int
}

func (f *fakeUnwrapper) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &kms.DecryptOutput{Plaintext: bytes.Clone(f.key)}, nil
}

// sealedTaskRoot writes a bundle sealed under key into a task root, exactly
// where a deployed package carries it, and returns the root.
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

// TestBakedVarsEnv_InjectsUnderTheNamespacedNameOnly proves the membrane hands
// the application its encrypted-baked values without ever putting them under
// the name the user chose: the namespaced name is the whole reason a value
// that was meant to be encrypted cannot be read out of a process environment
// dump as though it were plaintext.
func TestBakedVarsEnv_InjectsUnderTheNamespacedNameOnly(t *testing.T) {
	root := sealedTaskRoot(t, dataKey(), map[string]string{"STRIPE_API_KEY": "sk-live", "WEBHOOK_SECRET": "whsec"})
	unwrap := &fakeUnwrapper{key: dataKey()}

	env, err := bakedVarsEnv(context.Background(), unwrap, base64.StdEncoding.EncodeToString([]byte("wrapped")), root)
	if err != nil {
		t.Fatalf("bakedVarsEnv: %v", err)
	}
	if unwrap.calls != 1 {
		t.Errorf("unwrapped the data key %d times, want exactly once at init", unwrap.calls)
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

// TestBakedVarsEnv_NoEnvelopeIsNoWorkAtAll proves a function that bakes
// nothing neither reads a file nor reaches KMS, so the class costs nothing at
// init for the apps that do not use it.
func TestBakedVarsEnv_NoEnvelopeIsNoWorkAtAll(t *testing.T) {
	unwrap := &fakeUnwrapper{key: dataKey()}

	env, err := bakedVarsEnv(context.Background(), unwrap, "", t.TempDir())
	if err != nil {
		t.Fatalf("bakedVarsEnv: %v", err)
	}
	if env != nil {
		t.Errorf("env = %v, want nothing", env)
	}
	if unwrap.calls != 0 {
		t.Errorf("KMS was called %d times with no envelope", unwrap.calls)
	}
}

// TestBakedVarsEnv_EveryFailureIsDiagnosable proves init fails, and says why,
// rather than starting an application whose variables are quietly unset — the
// failure mode a value read as a plain property could never reveal.
func TestBakedVarsEnv_EveryFailureIsDiagnosable(t *testing.T) {
	envelope := base64.StdEncoding.EncodeToString([]byte("wrapped"))
	sealed := sealedTaskRoot(t, dataKey(), map[string]string{"A": "one"})

	cases := []struct {
		name    string
		unwrap  *fakeUnwrapper
		env     string
		root    string
		wantAny []string
	}{
		{
			name:    "the package carries no sealed file",
			unwrap:  &fakeUnwrapper{key: dataKey()},
			env:     envelope,
			root:    t.TempDir(),
			wantAny: []string{baked.FilePath},
		},
		{
			name:    "the envelope is not an envelope",
			unwrap:  &fakeUnwrapper{key: dataKey()},
			env:     "not base64!",
			root:    sealed,
			wantAny: []string{baked.EnvelopeVar},
		},
		{
			name:    "the key cannot be unwrapped",
			unwrap:  &fakeUnwrapper{err: errors.New("AccessDeniedException")},
			env:     envelope,
			root:    sealed,
			wantAny: []string{"AccessDeniedException"},
		},
		{
			name:    "the key is not the one the bundle was sealed under",
			unwrap:  &fakeUnwrapper{key: bytes.Repeat([]byte{1}, baked.KeyBytes)},
			env:     envelope,
			root:    sealed,
			wantAny: []string{"decrypt baked variables"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := bakedVarsEnv(context.Background(), tc.unwrap, tc.env, tc.root)
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
