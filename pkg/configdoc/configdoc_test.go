package configdoc

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeNamesTheComputeAnOptionUnderTheOtherComputeBelongsTo(t *testing.T) {
	cases := []struct {
		compute string
		want    string
	}{
		{`{"serverless":{"health":{"path":"/up"}}}`, "health is an option of container compute"},
		{`{"serverless":{"instances":{"min":1}}}`, "instances is an option of container compute"},
		{`{"container":{"framework":"next"}}`, "framework is an option of serverless compute"},
		{`{"container":{"entrypoint":"server.js"}}`, "entrypoint is an option of serverless compute"},
	}
	for _, c := range cases {
		_, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"a","path":".","compute":`+c.compute+`}]}`), env(nil))
		var unknown UnknownKeyError
		if !errors.As(err, &unknown) {
			t.Fatalf("decode %s err = %v, want an unknown key", c.compute, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("error %q does not say %q", err, c.want)
		}
	}
}

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
		t.Fatalf("options = %s, want what the aws key contains", doc.Provider.Options)
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

func TestDecodeEdgeInEitherFormUnderTheProvider(t *testing.T) {
	for _, spelled := range []string{`"cloudfront"`, `{"cloudfront":{}}`} {
		doc, err := Decode([]byte(`{"slug":"acme","provider":{"aws":{"edge":`+spelled+`}}}`), env(nil))
		if err != nil {
			t.Fatalf("decode %s: %v", spelled, err)
		}
		if doc.Provider.Edge == nil || doc.Provider.Edge.ID != "cloudfront" {
			t.Fatalf("edge from %s = %+v, want cloudfront", spelled, doc.Provider.Edge)
		}
	}
}

func TestDecodeKeepsTheEdgesOptionsAsWrittenForTheEdgeToRead(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","provider":{"vps":{"ssh":"box","edge":{"cloudflare":{"tunnel":true}}}}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Provider.Edge == nil || doc.Provider.Edge.ID != "cloudflare" || string(doc.Provider.Edge.Options) != `{"tunnel":true}` {
		t.Fatalf("edge = %+v, want cloudflare with its options as written", doc.Provider.Edge)
	}
}

func TestDecodeHandsTheProviderItsOptionsWithoutTheEdgeAndDNS(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","provider":{"aws":{"region":"eu-west-2","edge":"cloudfront","dns":"route53"}}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(doc.Provider.Options) != `{"region":"eu-west-2"}` {
		t.Fatalf("options = %s, want the provider's own options alone", doc.Provider.Options)
	}
}

func TestDecodeDNSKeepsItsZoneUnderItsIdentifier(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","provider":{"aws":{"dns":{"route53":{"zone":"example.com"}}}}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Provider.DNS == nil || doc.Provider.DNS.ID != "route53" || doc.Provider.DNS.Options.Zone != "example.com" {
		t.Fatalf("dns = %+v, want route53 in example.com", doc.Provider.DNS)
	}

	doc, err = Decode([]byte(`{"slug":"acme","provider":{"aws":{"dns":"cloudflare"}}}`), env(nil))
	if err != nil {
		t.Fatalf("decode shorthand: %v", err)
	}
	if doc.Provider.DNS == nil || doc.Provider.DNS.ID != "cloudflare" || doc.Provider.DNS.Options.Zone != "" {
		t.Fatalf("dns = %+v, want cloudflare with no zone", doc.Provider.DNS)
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
		{"a provider whose required option is left out", `"provider":{"vps":{}}`, []string{`"provider.vps"`, `"ssh"`, "The machine to deploy onto"}},
		{"a provider whose required option is null", `"provider":{"vps":{"ssh":null}}`, []string{`"provider.vps"`, `"ssh"`}},
		{"a provider missing one of its required options", `"provider":{"gcp":{"project":"p"}}`, []string{`"provider.gcp"`, `"region"`}},
		{"a provider whose options are required, set to null", `"provider":{"vps":null}`, []string{`"provider.vps" must be an object of options`}},
		{"a provider that may be named alone, set to null", `"provider":{"aws":null}`, []string{`"provider.aws" must be an object of options`}},
		{"an edge set to null", `"provider":{"aws":{"edge":{"cloudfront":null}}}`, []string{`"provider.aws.edge.cloudfront" must be an object of options`}},
		{"a provider that is neither", `"provider":7`, []string{`"provider"`, "aws, gcp, vps"}},
		{"an edge keyed twice", `"provider":{"aws":{"edge":{"cloudfront":{},"cloudflare":{}}}}`, []string{`"provider.aws.edge"`, "cloudflare", "cloudfront"}},
		{"an edge nobody fronts with", `"provider":{"aws":{"edge":"fastly"}}`, []string{`"fastly"`, "aws cannot front deployments with", "api-gateway, cloudflare, cloudfront"}},
		{"an edge another provider fronts with", `"provider":{"gcp":{"project":"p","region":"r","edge":"cloudfront"}}`, []string{`"provider.gcp.edge"`, `"cloudfront"`, "gcp cannot front deployments with", "alb, cloudflare"}},
		{"the box, which is no edge", `"provider":{"vps":{"ssh":"box","edge":"box"}}`, []string{`"box"`, "vps cannot front deployments with", "cloudflare"}},
		{"Cloud Run's own url, which is no edge", `"provider":{"gcp":{"project":"p","region":"r","edge":{"direct":{}}}}`, []string{`"direct"`, "alb, cloudflare"}},
		{"a dns nobody writes with", `"provider":{"aws":{"dns":{"gandi":{}}}}`, []string{`"gandi"`, "aws cannot write hostname records with", "cloudflare, route53"}},
		{"a dns another provider writes with", `"provider":{"vps":{"ssh":"box","dns":"route53"}}`, []string{`"provider.vps.dns"`, `"route53"`, "vps cannot write hostname records with", "cloudflare"}},
		{"a dns turned on", `"provider":{"aws":{"dns":true}}`, []string{`"provider.aws.dns"`, "cloudflare, route53"}},
		{"provider options that are not an object", `"provider":{"aws":7}`, []string{`"provider.aws"`, "an object"}},
		{"edge options that are not an object", `"provider":{"aws":{"edge":{"cloudfront":"on"}}}`, []string{`"provider.aws.edge.cloudfront"`, "an object"}},
		{"dns options that are not an object", `"provider":{"aws":{"dns":{"route53":["example.com"]}}}`, []string{`"provider.aws.dns.route53"`, "an object"}},
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
		{"dns options", `{"slug":"acme","provider":{"aws":{"dns":{"route53":{"zonee":"x"}}}}}`, "provider.aws.dns.route53.zonee"},
		{"an edge at the top level, where it no longer goes", `{"slug":"acme","edge":"cloudfront"}`, "edge"},
		{"a framework under a container", `{"slug":"acme","apps":[{"name":"web","path":".","compute":{"container":{"framework":"next"}}}]}`, "apps[0].compute.container.framework"},
		{"container options under serverless", `{"slug":"acme","apps":[{"name":"web","path":".","compute":{"serverless":{"health":{"path":"/up"}}}}]}`, "apps[0].compute.serverless.health"},
		{"a container option on the app itself", `{"slug":"acme","apps":[{"name":"web","path":".","minInstances":1}]}`, "apps[0].minInstances"},
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
	doc, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"a","path":".","compute":{"serverless":{"framework":"go","entrypoint":"cmd/server"}}},{"name":"b","path":".","compute":{"serverless":{"framework":"node"}},"arch":"arm64"}]}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a := doc.Apps[0]; a.Compute.Serverless.Framework != "go" || a.Compute.Serverless.Entrypoint != "cmd/server" || a.Arch != "" {
		t.Fatalf("app a = %+v", a.Compute.Serverless)
	}
	if b := doc.Apps[1]; b.Compute.Serverless.Framework != "node" || b.Arch != "arm64" {
		t.Fatalf("app b = %+v", b.Compute.Serverless)
	}
}

func TestDecodeComputeNamedAloneOrKeyed(t *testing.T) {
	cases := []struct {
		spelled               string
		serverless, container bool
	}{
		{`"serverless"`, true, false},
		{`"container"`, false, true},
		{`{"serverless":{}}`, true, false},
		{`{"container":{}}`, false, true},
		{`{"container":null}`, false, true},
	}
	for _, c := range cases {
		doc, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"a","path":".","compute":`+c.spelled+`}]}`), env(nil))
		if err != nil {
			t.Fatalf("decode %s: %v", c.spelled, err)
		}
		compute := doc.Apps[0].Compute
		if (compute.Serverless != nil) != c.serverless || (compute.Container != nil) != c.container {
			t.Errorf("compute from %s = %+v, want serverless %v container %v", c.spelled, compute, c.serverless, c.container)
		}
	}
}

func TestDecodeRefusesAComputeThatIsNotOneKnownCompute(t *testing.T) {
	cases := []struct {
		name string
		json string
		want []string
	}{
		{"a compute nobody runs", `"compute":"edge"`, []string{`"apps[0].compute"`, `"serverless", "container"`}},
		{"a compute keyed twice", `"compute":{"serverless":{},"container":{}}`, []string{`"apps[0].compute"`, "exactly one"}},
		{"a compute keyed by nothing", `"compute":{}`, []string{`"apps[0].compute"`, "exactly one"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"a","path":".",`+c.json+`}]}`), env(nil))
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

func TestDecodeEscapesOnlyAnOpeningInterpolation(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"web","path":"a$$b$"}]}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Apps[0].Path != "a$$b$" {
		t.Fatalf("path = %q, want the dollars kept where no ${ follows them", doc.Apps[0].Path)
	}
}

func TestDecodeKeepsASecretFieldAsItsPlaceholder(t *testing.T) {
	doc, err := Decode(
		[]byte(`{"slug":"acme","registry":{"server":"ghcr.io","password":"${GHCR_TOKEN}"}}`),
		env(map[string]string{"GHCR_TOKEN": "ghp_secret"}),
	)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Registry.Password != "${GHCR_TOKEN}" {
		t.Fatalf("password = %q, want the placeholder left for the CLI to resolve where it pushes", doc.Registry.Password)
	}
}

func TestDecodeKeepsASecretPlaceholderWhoseVariableIsUnset(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","registry":{"server":"ghcr.io","password":"${GHCR_TOKEN}"}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Registry.Password != "${GHCR_TOKEN}" {
		t.Fatalf("password = %q", doc.Registry.Password)
	}
}

func TestDecodeRefusesASecretFieldThatIsNotOnePlaceholder(t *testing.T) {
	cases := []struct {
		name     string
		password string
		want     []string
		unspoken string
	}{
		{name: "a bare variable name", password: "GHCR_TOKEN", want: []string{`"registry.password"`, `"${GHCR_TOKEN}"`}},
		{name: "the secret itself", password: "ghp_16C7e42F292c6912E7710c838347Ae178B4a", want: []string{`"registry.password"`, `"${REGISTRY_TOKEN}"`, "buildEnv"}, unspoken: "ghp_16C7e42F292c6912E7710c838347Ae178B4a"},
		{name: "a partial interpolation", password: "ghp_${SUFFIX}", want: []string{`"registry.password"`, "whole value"}, unspoken: "ghp_"},
		{name: "two placeholders", password: "${A}${B}", want: []string{`"registry.password"`, "whole value"}},
		{name: "an escaped placeholder", password: "$${GHCR_TOKEN}", want: []string{`"registry.password"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode(
				[]byte(`{"slug":"acme","registry":{"server":"ghcr.io","password":"`+c.password+`"}}`),
				env(map[string]string{"GHCR_TOKEN": "x", "SUFFIX": "y", "A": "a", "B": "b"}),
			)
			if err == nil {
				t.Fatalf("decoded password %q without error", c.password)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %s", err, want)
				}
			}
			if c.unspoken != "" && strings.Contains(err.Error(), c.unspoken) {
				t.Errorf("error %q repeats the secret", err)
			}
		})
	}
}

func TestAddedKnownIDsDecodeUntilRestored(t *testing.T) {
	document := []byte(`{"slug":"acme","provider":{"reference":{"edge":"front","dns":"zone"}}}`)

	restore := AddKnownIDs("reference", []string{"front"}, []string{"zone"})
	doc, err := Decode(document, env(nil))
	if err != nil {
		t.Fatalf("decode with the ids added: %v", err)
	}
	if doc.Provider.ID != "reference" || doc.Provider.Edge.ID != "front" || doc.Provider.DNS.ID != "zone" {
		t.Errorf("decoded provider %q edge %q dns %q, want the added ids", doc.Provider.ID, doc.Provider.Edge.ID, doc.Provider.DNS.ID)
	}

	restore()
	if _, err := Decode(document, env(nil)); err == nil || !strings.Contains(err.Error(), "knows no such") {
		t.Errorf("decode after restore err = %v, want the added ids unknown again", err)
	}
}

func TestDecodeKeepsTheInstanceCountsAContainerAppNames(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"a","path":".","compute":{"container":{"instances":{"min":0,"max":4}}}},{"name":"b","path":".","compute":"container"}]}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	instances := doc.Apps[0].Compute.Container.Instances
	if instances == nil || instances.Min == nil || *instances.Min != 0 || instances.Max == nil || *instances.Max != 4 {
		t.Fatalf("app a = %+v", instances)
	}
	if doc.Apps[1].Compute.Container.Instances != nil {
		t.Fatalf("app b names no instance counts, and decoded %+v", doc.Apps[1].Compute.Container.Instances)
	}
}

func TestDecodeRefusesAnInstanceCountThatIsNotAWholeNumber(t *testing.T) {
	_, err := Decode([]byte(`{"slug":"acme","apps":[{"name":"a","path":".","compute":{"container":{"instances":{"max":2.5}}}}]}`), env(nil))
	if err == nil {
		t.Fatal("decoded 2.5 instances")
	}
	if !strings.Contains(err.Error(), `"apps[0].compute.container.instances.max" must be a whole number`) {
		t.Fatalf("error %q does not name the key and a whole number", err)
	}
}
