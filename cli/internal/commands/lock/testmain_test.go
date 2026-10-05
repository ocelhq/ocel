package lock

import (
	"context"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	clitest.UnsetColorEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

func newTestDependencies(pin func(ctx context.Context, projectDir string) (lockfile.Lock, error)) Dependencies {
	return Dependencies{Invocation: clitest.NewInvocation(), Pin: pin}
}
