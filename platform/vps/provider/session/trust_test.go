package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func generated(t *testing.T, kind string) providerkit.HostKey {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host_key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", kind, "-N", "", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -t %s: %v\n%s", kind, err, out)
	}
	published, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(published))
	key := providerkit.HostKey{Type: fields[0], Key: fields[1]}
	if !fingerprint(&key) {
		t.Fatalf("could not fingerprint the %s key ssh-keygen just wrote", kind)
	}
	return key
}

func TestOurFingerprintIsTheOneSshKeygenPrints(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"ed25519", "rsa", "ecdsa"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			key := generated(t, kind)
			entry := filepath.Join(t.TempDir(), "known_hosts")
			if err := os.WriteFile(entry, []byte("host "+key.Type+" "+key.Key+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			printed, err := exec.Command("ssh-keygen", "-lf", entry).Output()
			if err != nil {
				t.Fatalf("ssh-keygen -lf: %v", err)
			}
			want := strings.Fields(string(printed))[1]
			if key.Fingerprint != want {
				t.Errorf("fingerprint() = %s, want ssh-keygen's %s", key.Fingerprint, want)
			}
		})
	}
}

func destination() Destination {
	return Destination{
		Written:    "web-1",
		Address:    "203.0.113.10",
		Port:       2222,
		User:       "ada",
		KnownHosts: []string{"/home/ada/.ssh/known_hosts"},
	}
}

func TestAKeyTheUserAlreadyHoldsAnchorsTheSession(t *testing.T) {
	t.Parallel()

	key := generated(t, "ed25519")
	anchor, trust := classify(destination(), []providerkit.HostKey{key}, known{keys: []providerkit.HostKey{key}})
	if trust != nil {
		t.Fatalf("classify() refused %+v over a key the user already holds", trust)
	}
	if anchor != key {
		t.Errorf("classify() anchored on %+v, want the key known_hosts holds", anchor)
	}
}

func TestAHostNobodyHasSeenIsAnUnknownHostKey(t *testing.T) {
	t.Parallel()

	key := generated(t, "ed25519")
	anchor, trust := classify(destination(), []providerkit.HostKey{key}, known{})
	if trust == nil {
		t.Fatal("classify() trusted a host held in no known_hosts file")
	}
	if trust.Reason != providerkit.UnknownHostKey {
		t.Errorf("classify() called it %s, want %s", trust.Reason, providerkit.UnknownHostKey)
	}
	if trust.Got != key || trust.Want != (providerkit.HostKey{}) {
		t.Errorf("classify() reported got %+v want %+v, want the offered key and nothing held", trust.Got, trust.Want)
	}
	if trust.Address != "203.0.113.10" || trust.Port != 2222 || trust.Host != "web-1" {
		t.Errorf("classify() lost the resolved destination: %+v", trust)
	}
	if len(trust.KnownHosts) == 0 {
		t.Error("classify() named no known_hosts file, so the user cannot act on the refusal")
	}
	if anchor != (providerkit.HostKey{}) {
		t.Errorf("classify() anchored on %+v while refusing", anchor)
	}
}

func TestADifferentKeyOfTheSameTypeIsAMismatch(t *testing.T) {
	t.Parallel()

	held, offering := generated(t, "ed25519"), generated(t, "ed25519")
	_, trust := classify(destination(), []providerkit.HostKey{offering}, known{keys: []providerkit.HostKey{held}})
	if trust == nil {
		t.Fatal("classify() trusted a host offering a key that is not the one held")
	}
	if trust.Reason != providerkit.HostKeyMismatch || !trust.Terminal() {
		t.Errorf("classify() called it %s, want a terminal %s", trust.Reason, providerkit.HostKeyMismatch)
	}
	if trust.Got != offering || trust.Want != held {
		t.Errorf("classify() reported got %+v want %+v, want the offered and the held key", trust.Got, trust.Want)
	}
	if !strings.Contains(trust.Remedy(), "ssh-keygen -R") {
		t.Errorf("Remedy() = %q, want the ssh-keygen -R line", trust.Remedy())
	}
}

func TestTheStrongestOfferedKeyIsTheOneReported(t *testing.T) {
	t.Parallel()

	rsa, ed := generated(t, "rsa"), generated(t, "ed25519")
	_, trust := classify(destination(), []providerkit.HostKey{rsa, ed}, known{})
	if trust == nil {
		t.Fatal("classify() trusted a host held in no known_hosts file")
	}
	if trust.Got != ed {
		t.Errorf("classify() reported the %s key, want the ed25519 one ssh would settle on", trust.Got.Type)
	}
}

func TestOneKnownKeyAmongSeveralOfferedIsEnough(t *testing.T) {
	t.Parallel()

	rsa, ed := generated(t, "rsa"), generated(t, "ed25519")
	anchor, trust := classify(destination(), []providerkit.HostKey{rsa, ed}, known{keys: []providerkit.HostKey{rsa}})
	if trust != nil {
		t.Fatalf("classify() refused %+v while known_hosts holds one of the offered keys", trust)
	}
	if anchor != rsa {
		t.Errorf("classify() anchored on %+v, want the key the session verifies against", anchor)
	}
}

func TestACertifiedHostIsLeftToOpenSSH(t *testing.T) {
	t.Parallel()

	key := generated(t, "ed25519")
	anchor, trust := classify(destination(), []providerkit.HostKey{key}, known{markers: true})
	if trust != nil {
		t.Fatalf("classify() refused %+v over a host whose known_hosts entry is a certificate authority", trust)
	}
	if anchor != key {
		t.Errorf("classify() anchored on %+v, want the key the host offered", anchor)
	}
}

func TestKnownHostsLinesAreReadPastTheirCommentsAndMarkers(t *testing.T) {
	t.Parallel()

	key := generated(t, "ed25519")
	rendered := "# Host web-1 found: line 3\n@cert-authority web-1 " + key.Type + " " + key.Key + "\nweb-1 " + key.Type + " " + key.Key + "\n"
	keys := keysIn(rendered)
	if len(keys) != 1 || keys[0] != key {
		t.Errorf("keysIn() = %+v, want just the one plain entry", keys)
	}
	if !markedIn(rendered) {
		t.Error("markedIn() missed the @cert-authority line")
	}
}
