package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/box/sealkey"
)

type ran struct {
	code   int
	stdout string
	stderr string
}

func sealed(t *testing.T, root, stdin string, argv ...string) ran {
	t.Helper()
	var stdout, stderr strings.Builder
	code := sealCommand(append([]string{"--tier-root", root}, argv...), strings.NewReader(stdin), &stdout, &stderr)
	return ran{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func tierRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, string(environment.TierProduction)), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

var shopDatabase = string(seal.BindToProject("shop", "variable", "production", "*", "/", "DATABASE_URL").Bytes())

func TestTheSealCommandCreatesATierKeyAndOpensWhatItSeals(t *testing.T) {
	t.Parallel()
	root := tierRoot(t)

	if got := sealed(t, root, "", "production", "init"); got.code != 0 || got.stdout != "" {
		t.Fatalf("seal production init = %+v, want the key created", got)
	}
	info, err := os.Lstat(sealkey.Path(root, environment.TierProduction))
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("the key is %v, %v, want mode 0400", info, err)
	}
	if got := sealed(t, root, "", "production", "init"); got.code != 2 || !strings.HasPrefix(got.stderr, "ocel-live seal: ") || !strings.Contains(got.stderr, "already exists") {
		t.Errorf("a second init = %+v, want exit 2 saying the key already exists", got)
	}

	plaintext := base64.StdEncoding.EncodeToString([]byte("postgres://example"))
	sealing := sealed(t, root, plaintext+"\n", "production", "seal", shopDatabase)
	if sealing.code != 0 || !strings.HasSuffix(sealing.stdout, "\n") || strings.Count(sealing.stdout, "\n") != 1 {
		t.Fatalf("seal = %+v, want one line of base64", sealing)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(sealing.stdout, "\n"))
	if err != nil || len(raw) != 12+len("postgres://example")+16 {
		t.Fatalf("seal printed %q, %v, want the nonce, the ciphertext and the tag in base64", sealing.stdout, err)
	}
	if got := sealed(t, root, sealing.stdout, "production", "open", shopDatabase); got.code != 0 || got.stdout != plaintext+"\n" {
		t.Errorf("open = %+v, want %q", got, plaintext+"\n")
	}
	elsewhere := string(seal.BindToProject("other", "variable", "production", "*", "/", "DATABASE_URL").Bytes())
	if got := sealed(t, root, sealing.stdout, "production", "open", elsewhere); got.code != 2 || got.stdout != "" || !strings.HasPrefix(got.stderr, "ocel-live seal: ") {
		t.Errorf("open under other associated data = %+v, want exit 2 and nothing on stdout", got)
	}
}

func TestTheSealCommandOpensAValueTheHelperSealed(t *testing.T) {
	t.Parallel()
	root := tierRoot(t)
	key, err := base64.StdEncoding.DecodeString("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sealkey.Path(root, environment.TierProduction), key, 0o400); err != nil {
		t.Fatal(err)
	}
	at := string(seal.BindToProject("shop", "variable", "production", "staging", "/web", "STRIPE_API_KEY").Bytes())
	got := sealed(t, root, "oKGio6SlpqeoqaqrlXMjQSy9Z+ARAOShYg7UlbGBMatxpNpQTeD5bD/s\n", "production", "open", at)
	if want := base64.StdEncoding.EncodeToString([]byte("sk_live_secret")) + "\n"; got.code != 0 || got.stdout != want {
		t.Errorf("open = %+v, want %q", got, want)
	}
}

func TestTheSealCommandRefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()
	root := tierRoot(t)
	value := base64.StdEncoding.EncodeToString([]byte("x")) + "\n"
	for _, call := range []struct {
		stdin string
		argv  []string
	}{
		{"", nil},
		{"", []string{"production"}},
		{"", []string{"../production", "init"}},
		{"", []string{"Production", "init"}},
		{"", []string{"production", "init", shopDatabase}},
		{value, []string{"production", "wrap", shopDatabase}},
		{value, []string{"production", "seal"}},
		{value, []string{"production", "seal", shopDatabase, "extra"}},
		{value, []string{"preview", "seal", shopDatabase}},
		{"not base64!\n", []string{"production", "seal", shopDatabase}},
	} {
		if got := sealed(t, root, call.stdin, call.argv...); got.code != 2 || got.stdout != "" || !strings.HasPrefix(got.stderr, "ocel-live seal: ") {
			t.Errorf("seal %q = %+v, want exit 2 with the reason on stderr", call.argv, got)
		}
	}
}
