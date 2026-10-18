package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
)

func stubAgent(t *testing.T, body string) agentCipher {
	t.Helper()
	path := filepath.Join(t.TempDir(), "live")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return agentCipher{argv: []string{path, "seal"}}
}

func TestTheConnectorSealsThroughTheAgentsSealCommandForTheTierAndAssociatedData(t *testing.T) {
	t.Parallel()

	cipher := stubAgent(t, `printf '%s' "$*" | base64`+"\n")
	bound := seal.BindToProject("shop", "variable", "production", "*", "/", "DATABASE_URL")

	said, err := cipher.Seal(context.Background(), environment.TierProduction, bound, []byte("postgres://example"))
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if want := "seal production seal " + string(bound.Bytes()); string(said) != want {
		t.Errorf("the agent ran as %q, want %q: the sudoers line names exactly that command", said, want)
	}
}

func TestASealTheAgentRefusesNamesWhatTheAgentSaid(t *testing.T) {
	t.Parallel()

	cipher := stubAgent(t, "echo 'ocel-live seal: the key is 16 bytes, want 32' >&2\nexit 2\n")

	_, err := cipher.Open(context.Background(), environment.TierPreview, seal.Bind("variable"), []byte("sealed"))
	if err == nil || !strings.Contains(err.Error(), "the key is 16 bytes") {
		t.Errorf("Open() = %v, want the agent's own reason", err)
	}
}
