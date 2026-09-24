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

func TestDecodeProviderKeyedByItsIdentifier(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","provider":{"aws":{"region":"eu-west-2"}}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Provider == nil || doc.Provider.ID != "aws" {
		t.Fatalf("provider = %+v, want aws", doc.Provider)
	}
	if string(doc.Provider.Options) != `{"region":"eu-west-2"}` {
		t.Fatalf("options = %s, want what the aws key holds", doc.Provider.Options)
	}
}

func TestDecodeProviderShorthandTakesNoOptions(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","provider":"aws"}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Provider == nil || doc.Provider.ID != "aws" || string(doc.Provider.Options) != `{}` {
		t.Fatalf("provider = %+v, want aws with no options", doc.Provider)
	}
}

func TestDecodeEdgeInEitherForm(t *testing.T) {
	for _, spelled := range []string{`"cloudfront"`, `{"cloudfront":{}}`} {
		doc, err := Decode([]byte(`{"slug":"acme","edge":`+spelled+`}`), env(nil))
		if err != nil {
			t.Fatalf("decode %s: %v", spelled, err)
		}
		if doc.Edge == nil || doc.Edge.ID != "cloudfront" {
			t.Fatalf("edge from %s = %+v, want cloudfront", spelled, doc.Edge)
		}
	}
}

func TestDecodeDNSCarriesItsZoneUnderItsIdentifier(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","dns":{"route53":{"zone":"example.com"}}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.DNS == nil || doc.DNS.ID != "route53" || doc.DNS.Options.Zone != "example.com" {
		t.Fatalf("dns = %+v, want route53 in example.com", doc.DNS)
	}

	doc, err = Decode([]byte(`{"slug":"acme","dns":"cloudflare"}`), env(nil))
	if err != nil {
		t.Fatalf("decode shorthand: %v", err)
	}
	if doc.DNS == nil || doc.DNS.ID != "cloudflare" || doc.DNS.Options.Zone != "" {
		t.Fatalf("dns = %+v, want cloudflare with no zone", doc.DNS)
	}
}

func TestDecodeRefusesASelectorThatIsNotOneKnownIdentifier(t *testing.T) {
	cases := []struct {
		name string
		json string
		want []string
	}{
		{"a provider keyed by nothing", `"provider":{}`, []string{`"provider"`, "aws, gcp, vps"}},
		{"a provider keyed twice", `"provider":{"aws":{},"gcp":{"project":"p","region":"r"}}`, []string{`"provider"`, "aws", "gcp", "one"}},
		{"a provider nobody ships", `"provider":{"azure":{}}`, []string{`"azure"`, "aws, gcp, vps"}},
		{"a provider nobody ships, as a string", `"provider":"azure"`, []string{`"azure"`, "aws, gcp, vps"}},
		{"a provider whose options are required, as a string", `"provider":"gcp"`, []string{`"gcp"`, `{ "gcp": {`, "aws"}},
		{"a provider whose options are required, held empty", `"provider":{"vps":null}`, []string{`"vps"`, `{ "vps": {`}},
		{"a provider that is neither", `"provider":7`, []string{`"provider"`, "aws, gcp, vps"}},
		{"an edge keyed twice", `"edge":{"cloudfront":{},"cloudflare":{}}`, []string{`"edge"`, "cloudflare", "cloudfront"}},
		{"an edge nobody fronts with", `"edge":"fastly"`, []string{`"fastly"`, "alb, api-gateway, box, cloudflare, cloudfront, direct"}},
		{"a dns nobody writes with", `"dns":{"gandi":{}}`, []string{`"gandi"`, "cloudflare, route53"}},
		{"a dns turned on", `"dns":true`, []string{`"dns"`, "cloudflare, route53"}},
		{"provider options that are not an object", `"provider":{"aws":7}`, []string{`"provider.aws"`, "an object"}},
		{"edge options that are not an object", `"edge":{"cloudfront":"on"}`, []string{`"edge.cloudfront"`, "an object"}},
		{"dns options that are not an object", `"dns":{"route53":["example.com"]}`, []string{`"dns.route53"`, "an object"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(`{"slug":"acme",`+c.json+`}`), env(nil))
			if err == nil {
				t.Fatalf("decoded %s without error", c.json)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
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
		{"edge options", `{"slug":"acme","edge":{"cloudflare":{"zone":"x"}}}`, "edge.cloudflare.zone"},
		{"dns options", `{"slug":"acme","dns":{"route53":{"zonee":"x"}}}`, "dns.route53.zonee"},
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
		[]byte(`{"slug":"${SLUG}","apps":[{"name":"web","path":"./${DIR}"}],"provider":{"aws":{"region":"${REGION}"}}}`),
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
