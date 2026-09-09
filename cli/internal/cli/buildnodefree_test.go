package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/node"
)

func TestBuildingAGoProjectUnpacksNoNodeBundle(t *testing.T) {
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.24\n")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{
  "slug": "go-shop",
  "provider": { "name": "aws", "options": {} }
}
`)

	deps := clitest.NewDeps()
	clitest.StubBuild(&deps, nil)

	var stdout, stderr bytes.Buffer
	if err := runBuild(context.Background(), deps, root, &stdout, &stderr); err != nil {
		t.Fatalf("runBuild err = %v; stderr=%s", err, stderr.String())
	}
	if _, err := os.Stat(node.DistDir(root)); err == nil {
		t.Fatalf("%s was unpacked for a project holding no JavaScript", node.DistDir(root))
	}
}
