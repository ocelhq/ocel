package build

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/node"
)

func TestBuildingAGoProjectUnpacksNoNodeBundle(t *testing.T) {
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.24\n")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{
  "slug": "go-shop",
  "provider": { "fake": {} }
}
`)

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)

	var out bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	if err := runBuild(context.Background(), dependencies, root); err != nil {
		t.Fatalf("runBuild err = %v; out=%s", err, out.String())
	}
	if _, err := os.Stat(node.DistDir(root)); err == nil {
		t.Fatalf("%s was unpacked for a project with no JavaScript", node.DistDir(root))
	}
}
