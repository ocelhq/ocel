package doctor

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestDoctorPassesOnAGoProjectWithNoNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.24\n")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{
  "slug": "go-shop",
  "provider": { "fake": {} },
  "domains": { "production": "shop.example.com", "preview": "*.preview.acme.com" }
}
`)

	p := fake.NewForProject(fake.Options{}, root)
	clitest.Bootstrap(t, p, environment.TierProduction)
	clitest.Bootstrap(t, p, environment.TierPreview)
	clitest.ServeFake(t, p)
	t.Setenv(providerclient.ReadyTimeoutEnvVar, "5s")
	t.Setenv("PATH", t.TempDir())

	invocation := clitest.NewInvocation()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := Run(context.Background(), invocation, root, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "node") {
		t.Fatalf("a Go project was told about node:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Good to go.") {
		t.Fatalf("doctor did not pass on a Go project with no node:\n%s", stdout.String())
	}
}
