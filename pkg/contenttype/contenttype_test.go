package contenttype

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInferTypesAFileByItsNameBeforeItsExtension(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]string{
		"/robots.txt":           "text/plain",
		"/notes.txt":            "text/plain; charset=utf-8",
		"manifest.json":         "application/manifest+json",
		"/data.json":            "application/json; charset=utf-8",
		"_next/static/chunk.JS": "text/javascript; charset=utf-8",
		"/icons/apple-icon.png": "image/png",
		"/v1.0/README":          "application/octet-stream",
		"/data.unknownext":      "application/octet-stream",
		"/x.toString":           "application/octet-stream",
	} {
		if got := Infer(path); got != want {
			t.Errorf("Infer(%q) = %q, want %q", path, got, want)
		}
	}
}

var routerTableEntry = regexp.MustCompile(`"([^"]+)":\s*"([^"]+)"`)

func routerTable(t *testing.T, src, name string) map[string]string {
	t.Helper()
	start := strings.Index(src, "const "+name+" = table({")
	if start == -1 {
		t.Fatalf("assets.mts declares no %s table", name)
	}
	body := src[start:]
	end := strings.Index(body, "});")
	if end == -1 {
		t.Fatalf("assets.mts never closes its %s table", name)
	}
	table := map[string]string{}
	for _, m := range routerTableEntry.FindAllStringSubmatch(body[:end], -1) {
		table[m[1]] = m[2]
	}
	return table
}

func TestTablesAgreeWithTheNextRouterServeTimeTables(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile(filepath.Join("..", "..", "frameworks", "next", "router", "src", "assets.mts"))
	if err != nil {
		t.Fatalf("read the next router's assets.mts: %v", err)
	}
	for _, c := range []struct {
		name string
		want map[string]string
	}{
		{name: "CONTENT_TYPES", want: byExtension},
		{name: "METADATA_CONTENT_TYPES", want: byName},
	} {
		if got := routerTable(t, string(src), c.name); !maps.Equal(got, c.want) {
			t.Errorf("%s in assets.mts = %v, want %v", c.name, got, c.want)
		}
	}
}
