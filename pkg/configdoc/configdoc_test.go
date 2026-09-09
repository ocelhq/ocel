package configdoc

import (
	"strings"
	"testing"
)

func env(pairs map[string]string) Lookup {
	return func(name string) (string, bool) {
		value, ok := pairs[name]
		return value, ok
	}
}

func TestDecodeMinimalDocument(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme"}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Slug != "acme" {
		t.Fatalf("slug = %q, want %q", doc.Slug, "acme")
	}
}

func TestDecodeProviderReference(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","provider":{"name":"aws","options":{"region":"eu-west-2"}}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Provider == nil || doc.Provider.Name != "aws" {
		t.Fatalf("provider = %+v, want name aws", doc.Provider)
	}
	if string(doc.Provider.Options) != `{"region":"eu-west-2"}` {
		t.Fatalf("options = %s", doc.Provider.Options)
	}
}

func TestDecodeRejectsUnknownKeys(t *testing.T) {
	cases := []struct {
		name string
		json string
		path string
	}{
		{"top level", `{"slug":"acme","slugg":"x"}`, "slugg"},
		{"nested object", `{"slug":"acme","discovery":{"path":["declarations"]}}`, "discovery.path"},
		{"array element", `{"slug":"acme","apps":[{"name":"web","path":".","runtim":"go"}]}`, "apps[0].runtim"},
		{"registry", `{"slug":"acme","registry":{"server":"ghcr.io","token":"X"}}`, "registry.token"},
		{"edge", `{"slug":"acme","edge":{"kind":"cloudflare","zone":"x"}}`, "edge.zone"},
		{"a key the app surface dropped", `{"slug":"acme","apps":[{"name":"web","path":".","runtime":"go"}]}`, "apps[0].runtime"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(c.json), env(nil))
			if err == nil {
				t.Fatalf("decoded %s without error", c.json)
			}
			if !strings.Contains(err.Error(), c.path) {
				t.Fatalf("error %q does not name %q", err, c.path)
			}
		})
	}
}

func TestDecodeInterpolatesEveryString(t *testing.T) {
	doc, err := Decode(
		[]byte(`{"slug":"${SLUG}","apps":[{"name":"web","path":"./${DIR}"}],"provider":{"name":"aws","options":{"region":"${REGION}"}}}`),
		env(map[string]string{"SLUG": "acme", "DIR": "web", "REGION": "eu-west-2"}),
	)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Slug != "acme" {
		t.Fatalf("slug = %q", doc.Slug)
	}
	if doc.Apps[0].Path != "./web" {
		t.Fatalf("path = %q", doc.Apps[0].Path)
	}
	if string(doc.Provider.Options) != `{"region":"eu-west-2"}` {
		t.Fatalf("options = %s", doc.Provider.Options)
	}
}

func TestDecodeMissingVariableNamesKeyPath(t *testing.T) {
	_, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"web","path":"${APP_DIR}"}]}`), env(nil))
	if err == nil {
		t.Fatal("decoded a missing variable without error")
	}
	if !strings.Contains(err.Error(), "apps[0].path") {
		t.Fatalf("error %q does not name the key path", err)
	}
	if !strings.Contains(err.Error(), "APP_DIR") {
		t.Fatalf("error %q does not name the variable", err)
	}
}

func TestDecodeEscapesDoubleDollar(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"web","path":"$${NOT_A_VAR}"}]}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Apps[0].Path != "${NOT_A_VAR}" {
		t.Fatalf("path = %q", doc.Apps[0].Path)
	}
}

func TestDecodeKeepsTheFrameworkAndArchitectureAnAppNames(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"a","path":".","framework":"go"},{"name":"b","path":".","framework":"node","arch":"arm64"}]}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Apps[0].Framework != "go" || doc.Apps[0].Arch != "" {
		t.Fatalf("app a = %+v", doc.Apps[0])
	}
	if doc.Apps[1].Framework != "node" || doc.Apps[1].Arch != "arm64" {
		t.Fatalf("app b = %+v", doc.Apps[1])
	}
}

func TestDecodeKeepsDomainsInEitherForm(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","domains":{"production":"a.example.com","preview":"*.p.example.com"},"apps":[{"name":"a","path":".","domains":{"production":["b.example.com","c.example.com"]}}]}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc.Domains.Production) != 1 || doc.Domains.Production[0] != "a.example.com" {
		t.Fatalf("production = %v", doc.Domains.Production)
	}
	if doc.Domains.Preview != "*.p.example.com" {
		t.Fatalf("preview = %q", doc.Domains.Preview)
	}
	if len(doc.Apps[0].Domains.Production) != 2 {
		t.Fatalf("app production = %v", doc.Apps[0].Domains.Production)
	}
}

func TestDecodeRejectsWrongType(t *testing.T) {
	_, err := Decode([]byte(`{"slug":"acme","apps":{"name":"web"}}`), env(nil))
	if err == nil {
		t.Fatal("decoded an object where a list belongs")
	}
	if !strings.Contains(err.Error(), "apps") {
		t.Fatalf("error %q does not name the key path", err)
	}
}
