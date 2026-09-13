package connectorkit

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestASocketListenerIsReachableByTheProxyAndNobodyWider(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "connector.sock")
	ln, err := listen("unix://" + path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if ln.Addr().Network() != "unix" || ln.Addr().String() != path {
		t.Fatalf("listening on %s %s, want a unix socket at %s", ln.Addr().Network(), ln.Addr(), path)
	}
	held, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if held.Mode().Perm() != socketMode {
		t.Errorf("the socket stands at %04o, want %04o: the proxy connects over it and nothing else on the box should",
			held.Mode().Perm(), socketMode)
	}
}

func TestASocketARunBeforeThisOneLeftIsCleared(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "connector.sock")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}

	ln, err := listen("unix://" + path)
	if err != nil {
		t.Fatalf("listen over a socket a dead run left: %v", err)
	}
	defer ln.Close()
}

func TestASocketPathIsRequiredWhenOneIsNamed(t *testing.T) {
	t.Parallel()

	if _, err := listen("unix://"); err == nil {
		t.Fatal("listen(unix://) = nil, want a refusal naming no socket")
	}
}

func TestTheKeyIsMintedOnceAndThePublicHalfIsWhatIsPrinted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	config := filepath.Join(dir, "config.json")
	written, err := json.Marshal(Config{
		Console:        "https://ocel.app",
		ConnectorID:    "con_1",
		OrganizationID: "org_1",
		Target:         "vps/SHA256:AAAA/ocel",
		Grants:         []string{CapabilityEnvVarsRead},
		KeyPath:        key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, written, 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := PublicKey(config)
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(first)); err != nil {
		t.Errorf("PublicKey() = %q, which is no base64 key the console can verify against", first)
	}
	again, err := PublicKey(config)
	if err != nil {
		t.Fatalf("second PublicKey: %v", err)
	}
	if again != first {
		t.Errorf("the connector minted a second key (%q then %q), so the console would verify a heartbeat against the wrong half", first, again)
	}
	held, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if held.Mode().Perm() != 0o600 {
		t.Errorf("the private key stands at %04o, want 0600", held.Mode().Perm())
	}
}
