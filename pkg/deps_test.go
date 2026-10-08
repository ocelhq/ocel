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
	"github.com/ocelhq/ocel/pkg/arch",
	"github.com/ocelhq/ocel/pkg/buildoutput",
	"github.com/ocelhq/ocel/pkg/configdoc",
	"github.com/ocelhq/ocel/pkg/containerimage",
	"github.com/ocelhq/ocel/pkg/cron",
	"github.com/ocelhq/ocel/pkg/dotenv",
	"github.com/ocelhq/ocel/pkg/edge",
	"github.com/ocelhq/ocel/pkg/environment",
	"github.com/ocelhq/ocel/pkg/envsource",
	"github.com/ocelhq/ocel/pkg/variablestore",
	"github.com/ocelhq/ocel/pkg/variablestoreserver",
	"github.com/ocelhq/ocel/pkg/images",
	"github.com/ocelhq/ocel/pkg/keyvalue",
	"github.com/ocelhq/ocel/pkg/kvstore",
	"github.com/ocelhq/ocel/pkg/localrpc",
	"github.com/ocelhq/ocel/pkg/naming",
	"github.com/ocelhq/ocel/pkg/pricing",
	"github.com/ocelhq/ocel/pkg/processenv",
	"github.com/ocelhq/ocel/pkg/progress",
	"github.com/ocelhq/ocel/pkg/progressproto",
	"github.com/ocelhq/ocel/pkg/proto",
	"github.com/ocelhq/ocel/pkg/realtime",
	"github.com/ocelhq/ocel/pkg/refusal",
	"github.com/ocelhq/ocel/pkg/router",
	"github.com/ocelhq/ocel/pkg/seal",
	"github.com/ocelhq/ocel/pkg/stackrecords",
	"github.com/ocelhq/ocel/pkg/statedir",
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
		{name: "arch", pattern: "./arch/...", open: providerBuildsOn},
		{name: "buildoutput", pattern: "./buildoutput/...", open: providerBuildsOn},
		{name: "containerimage", pattern: "./containerimage/...", open: providerBuildsOn},
		{name: "cron", pattern: "./cron/...", open: []string{"github.com/ocelhq/ocel/pkg/cron"}},
		{name: "dotenv", pattern: "./dotenv/...", open: providerBuildsOn},
		{name: "environment", pattern: "./environment/...", open: []string{"github.com/ocelhq/ocel/pkg/environment"}},
		{name: "envsource", pattern: "./envsource/...", open: providerBuildsOn},
		{name: "variablestore", pattern: "./variablestore/...", open: providerBuildsOn},
		{name: "variablestoreserver", pattern: "./variablestoreserver/...", open: providerBuildsOn},
		{name: "images", pattern: "./images/...", open: providerBuildsOn},
		{name: "keyvalue", pattern: "./keyvalue/...", open: providerBuildsOn},
		{name: "kvstore", pattern: "./kvstore/...", open: []string{"github.com/ocelhq/ocel/pkg/kvstore", "github.com/ocelhq/ocel/pkg/proto"}},
		{name: "realtime", pattern: "./realtime/...", open: []string{"github.com/ocelhq/ocel/pkg/proto", "github.com/ocelhq/ocel/pkg/realtime"}},
		{name: "refusal", pattern: "./refusal/...", open: providerBuildsOn},
		{name: "router", pattern: "./router/...", open: []string{
			"github.com/ocelhq/ocel/pkg/edge",
			"github.com/ocelhq/ocel/pkg/environment",
			"github.com/ocelhq/ocel/pkg/progress",
			"github.com/ocelhq/ocel/pkg/refusal",
			"github.com/ocelhq/ocel/pkg/router",
		}},
		{name: "seal", pattern: "./seal/...", open: []string{"github.com/ocelhq/ocel/pkg/environment", "github.com/ocelhq/ocel/pkg/seal"}},
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
	repo + "pkg/provider/providerserver",
}

func TestNothingButAProviderImportsAPackageBeneathPkgProvider(t *testing.T) {
	t.Parallel()

	pkgs := depstest.Workspace(t)
	testOnly := importedOnlyByTests(pkgs)
	for _, c := range []struct {
		name    string
		imports func(depstest.Package) []string
		open    []string
	}{
		{name: "build", imports: func(p depstest.Package) []string {
			if testOnly[p.ImportPath] {
				return nil
			}
			return p.Imports
		}},
		{name: "test", imports: func(p depstest.Package) []string {
			if testOnly[p.ImportPath] {
				return slices.Concat(p.Imports, p.TestImports, p.XTestImports)
			}
			return slices.Concat(p.TestImports, p.XTestImports)
		}, open: providerTestSupport},
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

func importedOnlyByTests(pkgs []depstest.Package) map[string]bool {
	built := map[string]bool{}
	tested := map[string]bool{}
	for _, p := range pkgs {
		for _, imported := range p.Imports {
			built[imported] = true
		}
		for _, imported := range slices.Concat(p.TestImports, p.XTestImports) {
			tested[imported] = true
		}
	}
	only := map[string]bool{}
	for imported := range tested {
		if !built[imported] {
			only[imported] = true
		}
	}
	return only
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
