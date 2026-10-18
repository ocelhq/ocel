package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAPurgeSecretIsTheOneTheEdgeContractDerivesFromTheSameSeedAndPrefix(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "edge", "contract", "fixtures", "purge-secret.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Seed      string `json:"seed"`
		ISRPrefix string `json:"isrPrefix"`
		Secret    string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if got := DerivePurgeSecret(fixture.Seed, fixture.ISRPrefix); got != fixture.Secret {
		t.Errorf("DerivePurgeSecret(%q, %q) = %q, want %q: an origin signs a purge with what the edge derives, so the two must agree", fixture.Seed, fixture.ISRPrefix, got, fixture.Secret)
	}
}
