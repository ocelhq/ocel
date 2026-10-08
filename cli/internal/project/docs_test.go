package project_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tailscale/hujson"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

const docsDir = "www/content/docs"

const configTabGroup = "config"

var (
	fenceTitle    = regexp.MustCompile(`title="([^"]+)"`)
	fenceTab      = regexp.MustCompile(`\btab="([^"]+)"`)
	fenceTabGroup = regexp.MustCompile(`\btab-group="([^"]+)"`)
)

type block struct {
	page     string
	title    string
	tab      string
	tabGroup string
	body     string
}

func fenceAttribute(pattern *regexp.Regexp, meta string) string {
	if named := pattern.FindStringSubmatch(meta); named != nil {
		return named[1]
	}
	return ""
}

func tabGroupsIn(page, source string) [][]block {
	var groups [][]block
	var group []block
	var open *block
	var body []string
	for _, line := range strings.Split(source, "\n") {
		if open != nil {
			if strings.HasPrefix(line, "```") {
				open.body = strings.Join(body, "\n")
				group = append(group, *open)
				open, body = nil, nil
				continue
			}
			body = append(body, line)
			continue
		}
		if strings.HasPrefix(line, "```") {
			meta := strings.TrimPrefix(line, "```")
			if tab := fenceAttribute(fenceTab, meta); tab != "" {
				open = &block{page: page, title: fenceAttribute(fenceTitle, meta), tab: tab, tabGroup: fenceAttribute(fenceTabGroup, meta)}
				continue
			}
		}
		if strings.TrimSpace(line) != "" && len(group) > 0 {
			groups = append(groups, group)
			group = nil
		}
	}
	if len(group) > 0 {
		groups = append(groups, group)
	}
	return groups
}

func decodedDocument(t *testing.T, name string, source []byte) string {
	t.Helper()
	document, err := configdoc.Decode(source, func(variable string) (string, bool) { return "<" + variable + ">", true })
	if err != nil {
		t.Fatalf("%s does not decode: %v", name, err)
	}
	document.Schema = ""
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	return string(encoded)
}

func evaluatedProgram(t *testing.T, root, name, source string) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "packages", "ocel"), filepath.Join(dir, "node_modules", "ocel")); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, project.TSFileName)
	if err := os.WriteFile(program, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	evaluated, err := project.EvaluateTypeScript(context.Background(), program)
	if err != nil {
		t.Fatalf("%s does not evaluate: %v", name, err)
	}
	return evaluated
}

func mdxPages(t *testing.T, root string) map[string]string {
	t.Helper()
	pages := map[string]string{}
	dir := filepath.Join(root, docsDir)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".mdx") {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		page, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		pages[page] = string(source)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the docs: %v", err)
	}
	return pages
}

func configBlocks(t *testing.T, root string) []block {
	t.Helper()
	var blocks []block
	for page, source := range mdxPages(t, root) {
		var open *block
		var body []string
		for _, line := range strings.Split(source, "\n") {
			if !strings.HasPrefix(line, "```") {
				if open != nil {
					body = append(body, line)
				}
				continue
			}
			if open != nil {
				open.body = strings.Join(body, "\n")
				blocks = append(blocks, *open)
				open, body = nil, nil
				continue
			}
			named := fenceTitle.FindStringSubmatch(strings.TrimPrefix(line, "```"))
			if named == nil || !configTitled(named[1]) {
				continue
			}
			open = &block{page: page, title: named[1]}
		}
	}
	return blocks
}

func configTitled(title string) bool {
	return project.IsConfig(title) && !project.IsTypeScript(title)
}

func TestEveryDocumentedConfigValidatesAgainstTheSchema(t *testing.T) {
	root := fixturetest.RepoDir(t)
	schema := committedSchema(t, root)
	want := schemaID(t, root)
	shown := 0
	for _, example := range configBlocks(t, root) {
		shown++
		name := example.page + " › " + example.title
		document := documentOf(t, name, example.title, []byte(example.body))
		named := schemaNamed(example.title, []byte(example.body), document)
		if named != want {
			t.Errorf("%s names %q, want the committed schema %q", name, named, want)
		}
		if err := schema.Validate(document); err != nil {
			t.Errorf("%s does not validate against the committed schema: %v", name, err)
		}
	}
	if shown == 0 {
		t.Fatal("no documentation page shows an ocel.json")
	}
}

func TestEveryDocumentedOcelJSONIsTheTabBesideTheOcelConfigTSItEquals(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}
	root := fixturetest.RepoDir(t)
	paired := 0
	for page, source := range mdxPages(t, root) {
		for _, group := range tabGroupsIn(page, source) {
			var program, document *block
			for i := range group {
				switch {
				case project.IsTypeScript(group[i].title):
					program = &group[i]
				case configTitled(group[i].title) && !project.IsYAML(group[i].title):
					document = &group[i]
				}
			}
			if document == nil {
				continue
			}
			name := page + " › " + document.title
			if program == nil || !project.IsTypeScript(group[0].title) {
				t.Errorf("%s is not the tab beside an %s shown first", name, project.TSFileName)
				continue
			}
			if group[0].tabGroup != configTabGroup {
				t.Errorf("%s opens a tab group named %q, want %q so the format a reader picks holds on every page", page+" › "+program.title, group[0].tabGroup, configTabGroup)
			}
			paired++
			evaluated := evaluatedProgram(t, root, page+" › "+program.title, program.body)
			standard, err := hujson.Standardize([]byte(document.body))
			if err != nil {
				t.Fatalf("%s is not valid JSON: %v", name, err)
			}
			if got, want := decodedDocument(t, page+" › "+program.title, evaluated), decodedDocument(t, name, standard); got != want {
				t.Errorf("%s and the %s beside it are different configs:\n%s\n%s", name, program.title, want, got)
			}
		}
	}
	if paired == 0 {
		t.Fatal("no documentation page shows an ocel.json beside an ocel.config.ts")
	}
}

func TestEveryDocumentedOcelJSONSitsInATabGroup(t *testing.T) {
	root := fixturetest.RepoDir(t)
	for page, source := range mdxPages(t, root) {
		tabbed := map[string]int{}
		for _, group := range tabGroupsIn(page, source) {
			for _, shown := range group {
				tabbed[shown.body]++
			}
		}
		for _, shown := range configBlocks(t, root) {
			if shown.page != page || project.IsYAML(shown.title) {
				continue
			}
			if tabbed[shown.body] == 0 {
				t.Errorf("%s › %s is shown alone, and every ocel.json is the tab beside its %s", page, shown.title, project.TSFileName)
			}
		}
	}
}

func TestOnlyTheConfigurationPageNamesTheDefaultDiscoveryDirectory(t *testing.T) {
	root := fixturetest.RepoDir(t)
	pages := mdxPages(t, root)
	canonical := "configuration.mdx"
	named := regexp.MustCompile(`\b` + regexp.QuoteMeta(discovery.DefaultRootDirName) + `\b`)
	for page, source := range pages {
		if page != canonical && named.MatchString(source) {
			t.Errorf("%s names the default discovery directory instead of linking to %s", page, canonical)
		}
	}
	link := "/docs/configuration#discoverypaths"
	for _, page := range []string{"index.mdx", "sdk/index.mdx"} {
		if !strings.Contains(pages[page], link) {
			t.Errorf("%s does not link to %s", page, link)
		}
	}
}
