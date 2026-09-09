package projectconfig_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

const docsDir = "www/content/docs"

const typescriptPage = "typescript-config.mdx"

var fenceTitle = regexp.MustCompile(`title="([^"]+)"`)

type block struct {
	page  string
	title string
	body  string
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

func jsonBlocks(t *testing.T, root string) []block {
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
			info := strings.TrimPrefix(line, "```")
			if !strings.HasPrefix(info, "json") {
				continue
			}
			named := fenceTitle.FindStringSubmatch(info)
			if named == nil {
				continue
			}
			open = &block{page: page, title: named[1]}
		}
	}
	return blocks
}

func configTitled(title string) bool {
	return projectconfig.IsConfig(title) && !projectconfig.IsProgram(title)
}

func TestEveryDocumentedConfigValidatesAgainstTheSchema(t *testing.T) {
	root := fixturetest.RepoDir(t)
	schema := committedSchema(t, root)
	want := schemaID(t, root)
	shown := 0
	for _, example := range jsonBlocks(t, root) {
		if !configTitled(example.title) {
			continue
		}
		shown++
		name := example.page + " › " + example.title
		document := documentOf(t, name, []byte(example.body))
		named, _ := document.(map[string]any)["$schema"].(string)
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

func TestOnlyTheTypeScriptPageShowsAConfigWrittenAsAProgram(t *testing.T) {
	root := fixturetest.RepoDir(t)
	for page, source := range mdxPages(t, root) {
		if page == typescriptPage {
			continue
		}
		if named := projectconfig.ProgramNamedIn(source); named != "" {
			t.Errorf("%s shows %s, and only %s shows a config written as a program", page, named, typescriptPage)
		}
	}
	if _, err := os.Stat(filepath.Join(root, docsDir, typescriptPage)); err != nil {
		t.Fatalf("the page that shows %s is missing: %v", projectconfig.TSFileName, err)
	}
}

func TestAConfigWrittenAsAProgramIsFoundUnderAnyTarget(t *testing.T) {
	for _, page := range []string{
		"deploy with `ocel.config.ts` at the root",
		"the gcp target reads `ocel.gcp.config.ts` instead",
		"```ts title=\"ocel.vps.config.ts\"",
	} {
		if projectconfig.ProgramNamedIn(page) == "" {
			t.Errorf("%q shows a config written as a program and went unfound", page)
		}
	}
	if named := projectconfig.ProgramNamedIn("run `ocel deploy` against next.config.ts"); named != "" {
		t.Errorf("found %q in a page that shows no ocel config", named)
	}
}
