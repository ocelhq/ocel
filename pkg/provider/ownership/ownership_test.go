package ownership_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/ownership"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

var writtenOnlyByTheConformanceSuite = []string{"conformance"}

func listDeclaredRoots(t *testing.T, dir string) []string {
	t.Helper()
	var roots []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/proto/") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.ValueSpec:
				if selector, ok := n.Type.(*ast.SelectorExpr); ok && selector.Sel.Name == "Root" {
					for _, value := range n.Values {
						if literal, ok := value.(*ast.BasicLit); ok && literal.Kind == token.STRING {
							root, _ := strconv.Unquote(literal.Value)
							roots = append(roots, root)
						}
					}
				}
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && key.Name == "Root" {
					if literal, ok := n.Value.(*ast.BasicLit); ok && literal.Kind == token.STRING {
						root, _ := strconv.Unquote(literal.Value)
						roots = append(roots, root)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return roots
}

func TestEveryRecordRootOcelWritesHasAnOwner(t *testing.T) {
	t.Parallel()

	declared := listDeclaredRoots(t, filepath.Join("..", ".."))
	if len(declared) < 10 {
		t.Fatalf("found %d roots under pkg, want every root ocel declares: the scan reads nothing", len(declared))
	}
	classified := ownership.ListRoots()
	for _, root := range declared {
		if slices.Contains(writtenOnlyByTheConformanceSuite, root) {
			continue
		}
		if !slices.Contains(classified, records.Root(root)) {
			t.Errorf("records root %q is written under pkg and ownership names no owner for it: add its package's OwnerOf to ownership", root)
		}
	}
}

func stack(t *testing.T, value string) naming.StackName {
	t.Helper()
	name, err := naming.ParseStackName(value)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestEachKeyNamesTheProjectAndEnvironmentItBelongsTo(t *testing.T) {
	t.Parallel()
	preview, production := environment.TierPreview, environment.TierProduction
	shared := records.Owner{Shared: true}
	shop := records.Owner{Project: "shop"}
	pr7 := records.Owner{Project: "shop", Environment: "pr-7"}
	prod := records.Owner{Project: "shop", Environment: "prod"}
	cells := variablestore.ValuesPartition(variablestore.Scope{Project: "shop", Tier: preview})

	for name, tc := range map[string]struct {
		key  records.Key
		want records.Owner
	}{
		"a project":                     {stackrecords.ProjectKey(preview, "shop"), shop},
		"the parts a project uses":      {stackrecords.PartsUsedKey(preview, "shop"), shop},
		"a preview's stack":             {stackrecords.StackKey(preview, "shop", stack(t, "pr-7--web--r0a1b2c3d")), pr7},
		"production's infra stack":      {stackrecords.StackKey(production, "shop", stack(t, "prod--infra")), prod},
		"a stack's images":              {stackrecords.StacksPartition(preview, "shop").Key("images", "web", "pr-7--web--r0a1b2c3d"), pr7},
		"a stack's resources":           {stackrecords.StacksPartition(preview, "shop").Key("resources", "pr-7--web--r0a1b2c3d"), pr7},
		"a preview environment":         {stackrecords.EnvironmentKey(preview, "shop", "pr-7"), pr7},
		"a preview's lease":             {stackrecords.EnvironmentLeaseKey(preview, "shop", "pr-7"), pr7},
		"a project's lease":             {stackrecords.ProjectLeaseKey(preview, "shop"), shop},
		"a project's edge stack":        {stackrecords.EdgeStackKey(preview, "shop"), shop},
		"the preview wildcard":          {stackrecords.WildcardKey(preview), shared},
		"a bootstrap part":              {stackrecords.BootstrapPartKey(preview, "edge"), shared},
		"a project's want of a part":    {stackrecords.BootstrapWantKey(preview, "shop", "edge"), shop},
		"the preview key":               {stackrecords.NewPreviewKeyRecordKey(), shared},
		"the gates":                     {stackrecords.NewGatesRecordKey(production), shared},
		"production's ledger pointer":   {ledger.Partition(production, "shop").Key("pointers", "@production"), prod},
		"a preview's ledger pointer":    {ledger.Partition(preview, "shop").Key("pointers", "pr-7"), pr7},
		"a release record":              {ledger.Partition(preview, "shop").Key("records", "web", "r1"), records.Owner{Project: "shop", EnvironmentInValue: true}},
		"a preview's value":             {variablestore.CellKey(variablestore.Scope{Project: "shop", Tier: preview}, variablestore.Coordinate{Cell: variablestore.Cell{Key: "KEY"}, Environment: "pr-7"}), pr7},
		"a tier-wide value":             {variablestore.CellKey(variablestore.Scope{Project: "shop", Tier: preview}, variablestore.Coordinate{Cell: variablestore.Cell{Key: "KEY"}}), shop},
		"a value of every project":      {variablestore.CellKey(variablestore.Scope{Project: variablestore.EveryProject, Tier: preview}, variablestore.Coordinate{Cell: variablestore.Cell{Key: "KEY"}}), shared},
		"a preview value's history":     {cells.Key("history", "/", "KEY", "pr-7", "0000000001"), pr7},
		"a preview's binding record":    {cells.Key("bindings", "db", "records", "pr-7"), pr7},
		"a preview's binding value":     {cells.Key("bindings", "db", "values", "pr-7"), pr7},
		"a preview's binding owner":     {cells.Key("bindingowners", "db", "pr-7"), pr7},
		"a reference from a preview":    {variablestore.ReferencesPartition(variablestore.Scope{Project: "lib", Tier: preview}).Key("/", "KEY", "shop", "/", "KEY", "pr-7"), pr7},
		"an env source registration":    {records.Partition{Tier: preview, Root: "envsources"}.Key("shop"), shop},
		"an env source status":          {records.Partition{Tier: preview, Root: "envsourcestatus"}.Key("abc"), shared},
		"an env source sharer":          {records.Partition{Tier: preview, Root: "envsourcestatus"}.Key("abc", "projects", "shop"), shop},
		"the env source digest key":     {records.Partition{Tier: preview, Root: "envsourcedigestkey"}.Key("digestkey"), shared},
		"a shared physical":             {records.Partition{Tier: preview, Root: "sharedphysicals", Path: []string{"shop"}}.Key("db-1"), shop},
		"a project's deployed settings": {records.Partition{Tier: preview, Root: providerserver.RootDeployedSettings}.Key("shop"), shop},
		"a router's invalidation":       {records.Partition{Tier: preview, Root: stackrecords.RootRouters, Path: []string{"relay", "shop"}}.Key("invalidation"), shop},
		"a provider session":            {providerserver.SessionKey("tenant", "0123"), shared},
		"a relayed question":            {providerserver.QuestionKey(preview, "tenant", "shop.preview", "q1"), shared},
	} {
		got, known := ownership.OwnerOfKey(tc.key)
		if !known || got != tc.want {
			t.Errorf("%s: OwnerOfKey(%s) = (%+v, %v), want %+v", name, tc.key, got, known, tc.want)
		}
	}
}

func TestAKeyNoLayoutWritesHasNoOwner(t *testing.T) {
	t.Parallel()
	for _, key := range []records.Key{
		records.Partition{Tier: environment.TierPreview, Root: "elsewhere"}.Key("shop"),
		records.Partition{Tier: environment.TierPreview, Root: stackrecords.RootBootstrap}.Key("vendor-record", "shop"),
		stackrecords.StacksPartition(environment.TierPreview, "shop").Key("not a stack name"),
		ledger.Partition(environment.TierPreview, "shop").Key("elsewhere", "x"),
	} {
		if owner, known := ownership.OwnerOfKey(key); known {
			t.Errorf("OwnerOfKey(%s) = %+v, want no owner: nothing writes that key", key, owner)
		}
	}
}

func TestEachSealBindingNamesTheProjectAndEnvironmentItBelongsTo(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		bound seal.AssociatedData
		want  records.Owner
	}{
		"a preview's value":        {seal.BindToProject("shop", "variable", "preview", "pr-7", "/", "KEY"), records.Owner{Project: "shop", Environment: "pr-7"}},
		"a tier-wide value":        {seal.BindToProject("shop", "variable", "preview", "*", "/", "KEY"), records.Owner{Project: "shop"}},
		"a value of every project": {seal.BindToProject("*", "variable", "preview", "*", "/", "KEY"), records.Owner{Shared: true}},
		"a preview's binding":      {seal.BindToProject("shop", "binding", "preview", "pr-7", "db"), records.Owner{Project: "shop", Environment: "pr-7"}},
		"a release's secrets":      {seal.BindToProject("shop", "release secrets", "web", "r1"), records.Owner{Project: "shop", EnvironmentInValue: true}},
		"the env source digest":    {seal.Bind("env source digest key", "preview"), records.Owner{Shared: true}},
		"the gates":                {stackrecords.GatesAssociatedData, records.Owner{Shared: true}},
		"the preview key":          {seal.Bind("preview key"), records.Owner{Shared: true}},
		"a provider session":       {providerserver.SessionAssociatedData("tenant", "0123"), records.Owner{Shared: true}},
		"a relayed question":       {providerserver.QuestionAssociatedData("tenant", "s", "q"), records.Owner{Shared: true}},
	} {
		parsed, err := seal.ParseAssociatedData(tc.bound.Bytes())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, known := ownership.OwnerOfAssociatedData(parsed)
		if !known || got != tc.want {
			t.Errorf("%s: OwnerOfAssociatedData(%q) = (%+v, %v), want %+v", name, tc.bound.Bytes(), got, known, tc.want)
		}
	}
	if owner, known := ownership.OwnerOfAssociatedData(seal.Bind("something else")); known {
		t.Errorf("an unknown binding has owner %+v, want none", owner)
	}
}
