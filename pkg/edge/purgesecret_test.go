package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAPurgeSecretIsTheOneTheEdgeContractDerivesFromTheSameSeedAndStorageCoordinate(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "edge", "contract", "fixtures", "purge-secret.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Seed              string `json:"seed"`
		StorageCoordinate string `json:"storageCoordinate"`
		Secret            string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if got := DerivePurgeSecret(fixture.Seed, fixture.StorageCoordinate); got != fixture.Secret {
		t.Errorf("DerivePurgeSecret(%q, %q) = %q, want %q: an origin signs a purge with what the edge derives, so the two must agree", fixture.Seed, fixture.StorageCoordinate, got, fixture.Secret)
	}
}
