package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const alias = "ocel-live"

type harness struct {
	target     Target
	knownHosts string
	addr       string
}

func live(t *testing.T) harness {
	t.Helper()
	addr, user, key := os.Getenv("OCEL_INCUS_ADDR"), os.Getenv("OCEL_INCUS_USER"), os.Getenv("OCEL_INCUS_KEY")
	if addr == "" || user == "" || key == "" {
		t.Skip("no incus VM in the environment; run under `scripts/incus.sh run <name> -- go test ./...`")
	}

	dir := t.TempDir()
	knownHosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownHosts, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config")
	written := fmt.Sprintf("Host %s\n  HostName %s\n  User %s\n  IdentityFile %s\n  IdentitiesOnly yes\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile /dev/null\n",
		alias, addr, user, key, knownHosts)
	if err := os.WriteFile(config, []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	return harness{target: Target{Alias: alias, Config: config}, knownHosts: knownHosts, addr: addr}
}

func (h harness) trust(t *testing.T) providerkit.HostKey {
	t.Helper()
	scanned, err := exec.Command("ssh-keyscan", "-T", "10", h.addr).Output()
	if err != nil {
		t.Fatalf("ssh-keyscan %s: %v", h.addr, err)
	}
	if err := os.WriteFile(h.knownHosts, scanned, 0o600); err != nil {
		t.Fatal(err)
	}
	keys := preferred(keysIn(string(scanned)))
	if len(keys) == 0 {
		t.Fatalf("ssh-keyscan %s offered no host key", h.addr)
	}
	return keys[0]
}

func TestLiveAnUnknownHostIsRefusedWithEverythingTheUserNeeds(t *testing.T) {
	h := live(t)

	_, err := Open(context.Background(), h.target)
	trust, ok := providerkit.HostTrustOf(err)
	if !ok {
		t.Fatalf("Open() = %v, want an unknown-host-key refusal against an empty known_hosts", err)
	}
	if trust.Reason != providerkit.UnknownHostKey || trust.Terminal() {
		t.Errorf("Open() refused with %s, want a recoverable %s", trust.Reason, providerkit.UnknownHostKey)
	}
	if trust.Host != alias || trust.Address != h.addr || trust.Port != 22 {
		t.Errorf("Open() refused over %+v, want the alias as written and the resolved address", trust)
	}
	if trust.Got.Type == "" || trust.Got.Key == "" || !strings.HasPrefix(trust.Got.Fingerprint, "SHA256:") {
		t.Errorf("Open() refused carrying %+v, want the key type, blob and SHA256 fingerprint", trust.Got)
	}
	if len(trust.KnownHosts) == 0 || trust.KnownHosts[0] != h.knownHosts {
		t.Errorf("Open() named %v, want the known_hosts file ssh_config points at", trust.KnownHosts)
	}
}

func TestLiveAKeyTheUserHoldsOpensTheSession(t *testing.T) {
	h := live(t)
	key := h.trust(t)

	live, err := Open(context.Background(), h.target)
	if err != nil {
		t.Fatalf("Open() = %v, want a session against a host the user's known_hosts holds", err)
	}
	defer live.Close()

	if live.HostKey().Fingerprint != key.Fingerprint {
		t.Errorf("Fingerprint() = %s, want the key known_hosts holds, %s", live.HostKey().Fingerprint, key.Fingerprint)
	}
	if want := os.Getenv("OCEL_INCUS_USER") + "@" + alias; live.Destination().Principal() != want {
		t.Errorf("Principal() = %q, want %q", live.Destination().Principal(), want)
	}
	out, err := live.Run(context.Background(), "echo ready")
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if strings.TrimSpace(out) != "ready" {
		t.Errorf("Run() = %q, want the command's own output", out)
	}
}

func TestLiveATrustedSessionAddsNothingToTheUsersKnownHosts(t *testing.T) {
	h := live(t)
	h.trust(t)
	before, err := os.ReadFile(h.knownHosts)
	if err != nil {
		t.Fatal(err)
	}

	session, err := Open(context.Background(), h.target)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer session.Close()

	after, err := os.ReadFile(h.knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("known_hosts changed under the session:\nbefore %q\nafter  %q", before, after)
	}
}

func TestLiveAChangedHostKeyIsTerminal(t *testing.T) {
	h := live(t)
	offered := h.trust(t)
	other := generated(t, "ed25519")
	if err := os.WriteFile(h.knownHosts, []byte(h.addr+" "+other.Type+" "+other.Key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Open(context.Background(), h.target)
	trust, ok := providerkit.HostTrustOf(err)
	if !ok {
		t.Fatalf("Open() = %v, want a host-key-mismatch refusal", err)
	}
	if trust.Reason != providerkit.HostKeyMismatch || !trust.Terminal() {
		t.Errorf("Open() refused with %s, want a terminal %s", trust.Reason, providerkit.HostKeyMismatch)
	}
	if trust.Got.Fingerprint != offered.Fingerprint || trust.Want.Fingerprint != other.Fingerprint {
		t.Errorf("Open() refused with got %s want %s, want %s and %s", trust.Got.Fingerprint, trust.Want.Fingerprint, offered.Fingerprint, other.Fingerprint)
	}
	if !strings.Contains(trust.Remedy, "ssh-keygen -R "+h.addr) {
		t.Errorf("Remedy() = %q, want the ssh-keygen -R line for this host", trust.Remedy)
	}
}

func TestLivePreflightNamesTheHostAndItsElevation(t *testing.T) {
	h := live(t)
	h.trust(t)

	session, err := Open(context.Background(), h.target)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer session.Close()

	facts, err := session.Preflight(context.Background())
	if err != nil {
		t.Fatalf("Preflight() = %v, want the ubuntu cloud image to pass", err)
	}
	if !facts.Systemd {
		t.Error("Preflight() found no systemd on an ubuntu cloud image")
	}
	if !facts.Sudo && !facts.Root {
		t.Error("Preflight() found neither root nor passwordless sudo on an ubuntu cloud image")
	}
	if facts.OS == "" || facts.Arch == "" {
		t.Errorf("Preflight() = %+v, want the host's own account of itself", facts)
	}
}

func TestLiveALoginWithoutPasswordlessSudoIsRefused(t *testing.T) {
	h := live(t)
	h.trust(t)

	admin, err := Open(context.Background(), h.target)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer admin.Close()
	if _, err := admin.Run(context.Background(), lodger); err != nil {
		t.Fatalf("could not stand up a login without sudo: %v", err)
	}

	plain := h.target
	plain.User = "ocel-lodger"
	session, err := Open(context.Background(), plain)
	if err != nil {
		t.Fatalf("Open() as ocel-lodger = %v", err)
	}
	defer session.Close()

	_, err = session.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight() passed a login that can neither be root nor sudo without a password")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("Preflight() = %v, want a refusal naming sudo", err)
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeDenied {
		t.Errorf("Preflight() = %v, want a %s refusal", err, providerkit.CodeDenied)
	}
}

const lodger = `set -e
sudo useradd -m -s /bin/bash ocel-lodger 2>/dev/null || true
sudo install -d -m 0700 -o ocel-lodger -g ocel-lodger ~ocel-lodger/.ssh
sudo cp ~/.ssh/authorized_keys ~ocel-lodger/.ssh/authorized_keys
sudo chown ocel-lodger:ocel-lodger ~ocel-lodger/.ssh/authorized_keys`
