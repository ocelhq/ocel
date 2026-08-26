package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestResolutionIsSshsOwn(t *testing.T) {
	t.Parallel()

	dest, err := resolve(context.Background(), Target{Host: "203.0.113.10"})
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}
	if dest.Address != "203.0.113.10" || dest.Port != 22 {
		t.Errorf("resolve() = %+v, want ssh's own answer for a plain host", dest)
	}
	if dest.User == "" {
		t.Error("resolve() named no login, and ssh always resolves one")
	}
	if len(dest.KnownHosts) == 0 || !filepath.IsAbs(dest.KnownHosts[0]) {
		t.Errorf("resolve() = %+v, want the absolute known_hosts path ssh would verify against", dest.KnownHosts)
	}
}

func TestTheDestinationSpelledOutReachesSsh(t *testing.T) {
	t.Parallel()

	dest, err := resolve(context.Background(), Target{Host: "203.0.113.10", Port: 2022, User: "grace"})
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}
	if dest.Port != 2022 || dest.User != "grace" {
		t.Errorf("resolve() = %+v, want the port and user the options name", dest)
	}
}

func TestTheAliasIsRecordedAsWritten(t *testing.T) {
	t.Parallel()

	dest, err := resolve(context.Background(), Target{Alias: "web-1"})
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}
	if dest.Written != "web-1" {
		t.Errorf("resolve() recorded %q, want the alias as the user wrote it", dest.Written)
	}
}

func TestTheKnownHostsEntryBracketsOnlyUnusualPorts(t *testing.T) {
	t.Parallel()

	plain := Destination{Address: "203.0.113.10", Port: 22}
	if plain.entry() != "203.0.113.10" {
		t.Errorf("entry() = %q, want the bare host on port 22", plain.entry())
	}
	odd := Destination{Address: "203.0.113.10", Port: 2222}
	if odd.entry() != "[203.0.113.10]:2222" {
		t.Errorf("entry() = %q, want the bracketed form off port 22", odd.entry())
	}
}

func TestTheForgetLineNamesTheEntryAndTheFileTheUserWouldEditThemselves(t *testing.T) {
	t.Parallel()

	dest := Destination{
		Address:    "203.0.113.10",
		Port:       2222,
		KnownHosts: []string{"/home/ada/.ssh/known_hosts", "/etc/ssh/ssh_known_hosts"},
	}
	if want := "ssh-keygen -R '[203.0.113.10]:2222' -f /home/ada/.ssh/known_hosts"; dest.Forget() != want {
		t.Errorf("Forget() = %q, want %q", dest.Forget(), want)
	}

	plain := Destination{Address: "203.0.113.10", Port: 22}
	if want := "ssh-keygen -R 203.0.113.10 -f ~/.ssh/known_hosts"; plain.Forget() != want {
		t.Errorf("Forget() with no known_hosts resolved = %q, want %q", plain.Forget(), want)
	}
}

func TestAKeyAliasIsTheNameSshKeysOn(t *testing.T) {
	t.Parallel()

	config := filepath.Join(t.TempDir(), "ssh_config")
	written := "Host web-1\n  HostName 203.0.113.10\n  Port 2222\n  HostKeyAlias ocel-vps\n"
	if err := os.WriteFile(config, []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}

	dest, err := resolve(context.Background(), Target{Alias: "web-1", Config: config})
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}
	if dest.KeyAlias != "ocel-vps" {
		t.Errorf("resolve() read the key alias as %q, want the ocel-vps ssh keys on", dest.KeyAlias)
	}
	if dest.entry() != "ocel-vps" {
		t.Errorf("entry() = %q, want the alias verbatim, with no port bracketing", dest.entry())
	}

	_, trust := classify(dest, []providerkit.HostKey{{Type: "ssh-ed25519", Key: "AAAA"}}, known{})
	if trust == nil {
		t.Fatal("classify() = nil, want an unknown-host-key refusal")
	}
	if trust.KnownHostsEntry() != "ocel-vps" {
		t.Errorf("the refusal keys on %q, want the alias the CLI must record under", trust.KnownHostsEntry())
	}
}
