package host

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sealClass = "production"

func sealDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 on this machine, and the seal helper reaches openssl through it")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, sealClass), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func sealHelperAt(t *testing.T, root, stdin string, args ...string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "seal")
	if err := os.WriteFile(script, sealScript, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", append([]string{script, sealClass}, args...)...)
	cmd.Env = append(os.Environ(), "OCEL_SEAL_ROOT="+root)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	rendered, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(rendered), 0
	case errors.As(err, &exit):
		return string(rendered), exit.ExitCode()
	default:
		t.Fatalf("run the seal helper: %v\n%s", err, stderr.String())
		return "", 0
	}
}

func TestTheSealHelperMintsAKeyOnceAndMintsNothingOverIt(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if rendered, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init on a class carrying no key exited %d with %q", code, rendered)
	}

	key := filepath.Join(root, sealClass, "seal.key")
	held, err := os.Stat(key)
	if err != nil {
		t.Fatalf("init exited 0 and wrote no key: %v", err)
	}
	if held.Size() != sealKeyBytes {
		t.Errorf("the key is %d bytes, want %d: AES-256 is sealed to nothing narrower", held.Size(), sealKeyBytes)
	}
	if held.Mode().Perm() != sealKeyMode {
		t.Errorf("the key stands at %04o, want %04o: every secret on this host opens to whoever reads it", held.Mode().Perm(), sealKeyMode)
	}

	minted, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, code := sealHelperAt(t, root, "", "init"); code == 0 {
		t.Fatal("a second init exited 0, and a key minted over is every value sealed to the old one lost")
	}
	again, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(minted) {
		t.Error("a refused init moved the key anyway")
	}
}

var aCoordinate = []string{
	"--project", "shop",
	"--env", "*",
	"--folder", "/",
	"--name", "DATABASE_URL",
}

func TestTheSealHelperRoundTripsAValueAndOpensItNowhereElse(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}

	plaintext := "postgres://example"
	sealed, code := sealHelperAt(t, root, encoded(plaintext), append([]string{"seal"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}
	if strings.Contains(sealed, encoded(plaintext)) {
		t.Fatal("the helper answered a seal carrying the value it was handed")
	}

	opened, code := sealHelperAt(t, root, sealed, append([]string{"open"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("open at the coordinate sealed exited %d", code)
	}
	if got := decoded(t, opened); got != plaintext {
		t.Errorf("open answered %q, want %q", got, plaintext)
	}

	for name, moved := range map[string][]string{
		"another project":     {"--project", "other", "--env", "*", "--folder", "/", "--name", "DATABASE_URL"},
		"another environment": {"--project", "shop", "--env", "staging", "--folder", "/", "--name", "DATABASE_URL"},
		"another folder":      {"--project", "shop", "--env", "*", "--folder", "/apps/web", "--name", "DATABASE_URL"},
		"another key":         {"--project", "shop", "--env", "*", "--folder", "/", "--name", "API_KEY"},
		"another link":        {"--project", "shop", "--env", "*", "--folder", "/", "--name", "DATABASE_URL", "--link", "db"},
	} {
		if _, code := sealHelperAt(t, root, sealed, append([]string{"open"}, moved...)...); code == 0 {
			t.Errorf("a value sealed here opened at %s, so the coordinate authenticates nothing", name)
		}
	}
}

func TestTheSealHelperOpensNothingWhoseBytesMoved(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	sealed, code := sealHelperAt(t, root, encoded("postgres://example"), append([]string{"seal"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sealed))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, code := sealHelperAt(t, root, tampered, append([]string{"open"}, aCoordinate...)...); code == 0 {
		t.Fatal("a sealed value whose bytes moved opened anyway")
	}
}

func TestWhatTheSealHelperWritesIsAES256GCMOverTheKeyOnDisk(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	plaintext := "postgres://example"
	rendered, code := sealHelperAt(t, root, encoded(plaintext), append([]string{"seal"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		t.Fatal(err)
	}

	key, err := os.ReadFile(filepath.Join(root, sealClass, "seal.key"))
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():],
		[]byte("shop/"+sealClass+"/*/%2F//DATABASE_URL/"))
	if err != nil {
		t.Fatalf("what the helper sealed does not open as %s over the key it minted: %v", SealAlgorithm, err)
	}
	if string(opened) != plaintext {
		t.Errorf("what the helper sealed opens as %q, want %q", opened, plaintext)
	}
}

func decoded(t *testing.T, rendered string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		t.Fatalf("the helper answered %q, which nothing sealed", rendered)
	}
	return string(raw)
}
