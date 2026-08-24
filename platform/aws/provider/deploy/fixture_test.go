package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work found above the working directory")
		}
		dir = parent
	}
}

func nextCacheFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(repoRoot(t), "frameworks", "next", "cache", "fixtures", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

const testDeploymentID = "d1a2b3c4d5e6f708192a3b4c5d6e7f80"

func deploymentIDFor(label string) string {
	if naming.ValidateDeploymentID(label) == nil {
		return label
	}
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:16])
}

func deployedAs(deploymentID string) Identity { return fingerprinted(deploymentID, "") }

func fingerprinted(deploymentID, values string) Identity {
	return deployedInto(providerkit.ProductionEnv, deploymentID, values)
}

func deployedInto(environment, deploymentID, values string) Identity {
	id, err := NewIdentity(deploymentIDFor(deploymentID), environment, values)
	if err != nil {
		panic(err)
	}
	return id
}
