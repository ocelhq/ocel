package projectconfig

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const docsDir = "www/content/docs"

const typescriptPage = "typescript-config.mdx"

var fenceTitle = regexp.MustCompile(`title="([^"]+)"`)

type block struct {
	page  string
	title string
	body  string
}

func jsonBlocks(t *testing.T, root string) []block {
	t.Helper()
	var blocks []block
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
		var open *block
		var body []string
		for _, line := range strings.Split(string(source), "\n") {
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
		return nil
	})
	if err != nil {
		t.Fatalf("walk the docs: %v", err)
	}
	return blocks
}

func configTitled(title string) bool {
	_, _, ok := formOf(title)
	return ok && strings.HasSuffix(title, ".json")
}

func TestEveryDocumentedConfigValidatesAgainstTheSchema(t *testing.T) {
	root := repoDir(t)
	schema := committedSchema(t, root)
	want := schemaID(t, root)
	shown := 0
	for _, example := range jsonBlocks(t, root) {
		if !configTitled(example.title) {
			continue
		}
		shown++
		name := example.page + " › " + example.title
		if !strings.Contains(example.body, want) {
			t.Errorf("%s does not name the committed schema %q", name, want)
		}
		if err := schema.Validate(documentOf(t, name, []byte(example.body))); err != nil {
			t.Errorf("%s does not validate against the committed schema: %v", name, err)
		}
	}
	if shown == 0 {
		t.Fatal("no documentation page shows an ocel.json")
	}
}

func TestOnlyTheTypeScriptPageShowsTheTypeScriptConfig(t *testing.T) {
	root := repoDir(t)
	dir := filepath.Join(root, docsDir)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".mdx") {
			return err
		}
		page, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if page != typescriptPage && strings.Contains(string(source), TSFileName) {
			t.Errorf("%s shows %s, and only %s does", page, TSFileName, typescriptPage)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the docs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, typescriptPage)); err != nil {
		t.Fatalf("the page that shows %s is missing: %v", TSFileName, err)
	}
}
