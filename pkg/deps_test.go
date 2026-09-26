package pkg_test

import (
	"os/exec"
	"slices"
	"strings"
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
	"github.com/ocelhq/ocel/pkg/stackrecords",
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
		{name: "appbuild", pattern: "./appbuild/...", open: providerBuildsOn},
		{name: "arch", pattern: "./arch/...", open: providerBuildsOn},
		{name: "envvars", pattern: "./envvars/...", open: providerBuildsOn},
		{name: "envvarsserver", pattern: "./envvarsserver/...", open: providerBuildsOn},
		{name: "images", pattern: "./images/...", open: providerBuildsOn},
		{name: "records", pattern: "./records/...", open: providerBuildsOn},
		{name: "refusal", pattern: "./refusal/...", open: providerBuildsOn},
		{name: "stackrecords", pattern: "./stackrecords/...", open: providerBuildsOn},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			depstest.Check(t, c.pattern, c.open, closed)
		})
	}
}

func TestNoPackageSitsAtPkgRuntimeWhereItWouldShadowTheStandardRuntime(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-e", "./...").Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	for _, path := range strings.Fields(string(out)) {
		if path == "github.com/ocelhq/ocel/pkg/runtime" {
			t.Errorf("%s is a package, and every file importing it would name it runtime over the standard library's; pkg/runtime holds only packages beneath it", path)
		}
	}
}

const repo = "github.com/ocelhq/ocel/"

var providerTestSupport = []string{
	repo + "pkg/provider/fake",
	repo + "pkg/provider/enginetest",
}

func TestNothingButAProviderImportsAPackageBeneathPkgProvider(t *testing.T) {
	t.Parallel()

	pkgs := depstest.Workspace(t)
	for _, c := range []struct {
		name    string
		imports func(depstest.Package) []string
		open    []string
	}{
		{name: "build", imports: func(p depstest.Package) []string { return p.Imports }},
		{name: "test", imports: func(p depstest.Package) []string { return slices.Concat(p.TestImports, p.XTestImports) }, open: providerTestSupport},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, p := range pkgs {
				if provider(p.ImportPath) {
					continue
				}
				for _, imported := range c.imports(p) {
					if beneathPkgProvider(imported) && !depstest.Within(imported, c.open) {
						t.Errorf("%s imports %s, which sits beneath pkg/provider though more than providers use it; it belongs at pkg/ top level", p.ImportPath, imported)
					}
				}
			}
		})
	}
}

func TestAPackageOnlyProvidersImportSitsBeneathPkgProvider(t *testing.T) {
	t.Parallel()

	importers := map[string][]string{}
	for _, p := range depstest.Workspace(t) {
		for _, imported := range p.Imports {
			importers[imported] = append(importers[imported], p.ImportPath)
		}
	}
	for pkg, by := range importers {
		if !depstest.Within(pkg, []string{repo + "pkg"}) || depstest.Within(pkg, []string{repo + "pkg/provider", repo + "pkg/internal"}) {
			continue
		}
		if !slices.ContainsFunc(by, func(importer string) bool { return !provider(importer) }) {
			t.Errorf("only providers import %s; it belongs beneath pkg/provider", pkg)
		}
	}
}

func beneathPkgProvider(pkg string) bool {
	return strings.HasPrefix(pkg, repo+"pkg/provider/")
}

func provider(pkg string) bool {
	if depstest.Within(pkg, []string{repo + "pkg/provider"}) {
		return true
	}
	vendor, ok := strings.CutPrefix(pkg, repo+"platform/")
	if !ok {
		return false
	}
	parts := strings.Split(vendor, "/")
	return len(parts) >= 2 && parts[1] == "provider"
}
