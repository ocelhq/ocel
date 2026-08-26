package session

import (
	"context"
	"path/filepath"
	"testing"
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
