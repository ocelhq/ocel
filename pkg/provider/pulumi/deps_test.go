package pulumi_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/internal/depstest"
)

func TestThePulumiModuleImportsOnlyWhatTheCodebaseMapOpensToPkg(t *testing.T) {
	t.Parallel()

	depstest.Check(t, "./...", depstest.OpenToPkg, depstest.ClosedToPkg)
}
