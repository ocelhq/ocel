package pkg_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/internal/depstest"
)

var providerBuildsOn = []string{
	"github.com/ocelhq/ocel/pkg/provider",
	"github.com/ocelhq/ocel/pkg/appbuild",
	"github.com/ocelhq/ocel/pkg/arch",
	"github.com/ocelhq/ocel/pkg/channel",
	"github.com/ocelhq/ocel/pkg/configdoc",
	"github.com/ocelhq/ocel/pkg/constants",
	"github.com/ocelhq/ocel/pkg/envvars",
	"github.com/ocelhq/ocel/pkg/envvarsserver",
	"github.com/ocelhq/ocel/pkg/images",
	"github.com/ocelhq/ocel/pkg/naming",
	"github.com/ocelhq/ocel/pkg/pricing",
	"github.com/ocelhq/ocel/pkg/proto",
	"github.com/ocelhq/ocel/pkg/records",
	"github.com/ocelhq/ocel/pkg/refusal",
	"github.com/ocelhq/ocel/platform/edge/contract",
}

func TestPkgImportsOnlyWhatTheCodebaseMapOpensToIt(t *testing.T) {
	t.Parallel()

	closed := append([]string{"github.com/pulumi"}, depstest.ClosedToPkg...)
	for _, c := range []struct {
		name    string
		pattern string
		open    []string
	}{
		{name: "pkg", pattern: "./...", open: depstest.OpenToPkg},
		{name: "provider", pattern: "./provider/...", open: providerBuildsOn},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			depstest.Check(t, c.pattern, c.open, closed)
		})
	}
}
