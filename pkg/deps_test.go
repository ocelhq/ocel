package pkg_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/internal/depstest"
)

var providerkitBuildsOn = []string{
	"github.com/ocelhq/ocel/pkg/providerkit",
	"github.com/ocelhq/ocel/pkg/channel",
	"github.com/ocelhq/ocel/pkg/configdoc",
	"github.com/ocelhq/ocel/pkg/constants",
	"github.com/ocelhq/ocel/pkg/costkit",
	"github.com/ocelhq/ocel/pkg/naming",
	"github.com/ocelhq/ocel/pkg/proto",
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
		{name: "providerkit", pattern: "./providerkit/...", open: providerkitBuildsOn},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			depstest.Check(t, c.pattern, c.open, closed)
		})
	}
}
