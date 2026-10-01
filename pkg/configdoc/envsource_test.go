package configdoc

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
)

func TestDecodeEnvSourceKeyedByItsIdentifierPerTier(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","envSource":{
		"production":{"infisical":{"project":"p-1","environment":"prod","path":"/acme","auth":{"universal":{"clientId":{"$env":"INFISICAL_CLIENT_ID"},"clientSecret":{"$env":"INFISICAL_CLIENT_SECRET"}}},"write":"missing"}},
		"preview":"builtin",
		"dev":{"exec":{"command":["op","inject","{folder}"],"format":"dotenv"}}
	}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	tiers := doc.EnvSource.Tiers()
	production := tiers.Production
	if production.ID() != "infisical:p-1/prod" || !production.CanCreate() || production.CanUpdate() ||
		!slices.Equal(production.CredentialVariables(), []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"}) {
		t.Fatalf("production = %s %s", production.Kind(), production.Options())
	}
	if tiers.Preview.Kind() != envsource.Builtin {
		t.Fatalf("preview = %s, want builtin named alone", tiers.Preview.Kind())
	}
	if tiers.Dev.Kind() != "exec" || tiers.Dev.Reading() != envsource.ReadingWhereOcelRuns {
		t.Fatalf("dev = %s, want exec", tiers.Dev.Kind())
	}
}

func TestEveryTierLeftOffOrNamedAloneReadsItsDefaultEnvSource(t *testing.T) {
	for _, config := range []string{
		`{"slug":"acme"}`,
		`{"slug":"acme","envSource":{}}`,
		`{"slug":"acme","envSource":{"production":"builtin","preview":"builtin","dev":"dotenv"}}`,
	} {
		doc, err := Decode([]byte(config), env(nil))
		if err != nil {
			t.Fatalf("decode %s: %v", config, err)
		}
		if got := doc.EnvSource.Tiers(); !reflect.DeepEqual(got, envsource.DefaultTiers()) {
			t.Errorf("tiers from %s = %+v, want every tier on its default", config, got)
		}
	}
}

func TestEachTierCarriesTheOptionsItsEnvSourceIsKeyedBy(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","envSource":{
		"production":{"infisical":{"project":"p-1","environment":"prod","auth":{"universal":{"clientId":{"$env":"ID"},"clientSecret":{"$env":"SECRET"}}}}},
		"preview":{"infisical":{"project":"p-1","environment":"staging","path":"/acme/","host":"https://infisical.example.com/","write":"values","auth":{"identity":{"identityId":"ident"}}}},
		"dev":{"exec":{"command":["op","run","{folder}"],"format":"json"}}
	}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	tiers := doc.EnvSource.Tiers()
	for _, c := range []struct {
		tier       string
		descriptor envsource.Descriptor
		kind, want string
	}{
		{"production", tiers.Production, "infisical", `{"project":"p-1","environment":"prod","auth":{"universal":{"clientId":{"$env":"ID"},"clientSecret":{"$env":"SECRET"}}}}`},
		{"preview", tiers.Preview, "infisical", `{"project":"p-1","environment":"staging","path":"/acme/","host":"https://infisical.example.com/","write":"values","auth":{"identity":{"identityId":"ident"}}}`},
		{"dev", tiers.Dev, "exec", `{"command":["op","run","{folder}"],"format":"json"}`},
	} {
		var got, want any
		if err := json.Unmarshal(c.descriptor.Options(), &got); err != nil {
			t.Fatalf("%s options %s: %v", c.tier, c.descriptor.Options(), err)
		}
		if err := json.Unmarshal([]byte(c.want), &want); err != nil {
			t.Fatal(err)
		}
		if c.descriptor.Kind() != c.kind || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %s %s, want %s %s", c.tier, c.descriptor.Kind(), c.descriptor.Options(), c.kind, c.want)
		}
	}
	if !tiers.Preview.CanUpdate() || tiers.Preview.ID() != "infisical:p-1/staging" {
		t.Errorf("preview = %s, want it to update values in infisical:p-1/staging", tiers.Preview.ID())
	}
}

func TestEnvSourceSchemaNamesWhatEachTierMayRead(t *testing.T) {
	generated, err := Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	var schema struct {
		Properties struct {
			EnvSource struct {
				Properties map[string]struct {
					AllOf []struct {
						OneOf []struct {
							Enum     []string `json:"enum"`
							Required []string `json:"required"`
						} `json:"oneOf"`
					} `json:"allOf"`
				} `json:"properties"`
			} `json:"envSource"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(generated, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for tier, alone := range map[string]string{"production": "builtin", "preview": "builtin", "dev": "dotenv"} {
		var named, keyed []string
		for _, alternative := range schema.Properties.EnvSource.Properties[tier].AllOf[0].OneOf {
			named = append(named, alternative.Enum...)
			keyed = append(keyed, alternative.Required...)
		}
		if !slices.Equal(named, []string{alone}) {
			t.Errorf("envSource.%s named alone = %v, want only %s", tier, named, alone)
		}
		if !slices.Equal(keyed, []string{"exec", "infisical"}) {
			t.Errorf("envSource.%s keyed by %v, want exec and infisical", tier, keyed)
		}
	}
}

func TestEachEnvSourceTypeIsDocumentedAsItselfNotAsAFieldThatUsesIt(t *testing.T) {
	docs := namedTypeDocs(t)
	for title, own := range map[string]string{
		"EnvSourceDescriptor":    EnvSourceDescriptor{}.Doc(),
		"DevEnvSourceDescriptor": DevEnvSourceDescriptor{}.Doc(),
		"InfisicalOptions":       envsource.InfisicalOptions{}.Doc(),
		"InfisicalAuth":          envsource.InfisicalAuth{}.Doc(),
		"UniversalAuth":          envsource.UniversalAuth{}.Doc(),
		"IdentityAuth":           envsource.IdentityAuth{}.Doc(),
		"ExecOptions":            envsource.ExecOptions{}.Doc(),
	} {
		if got := docs[title]; !slices.Equal(got, []string{own}) {
			t.Errorf("%s is described as %q, want only its own %q", title, got, own)
		}
	}
}

func TestDecodeRefusesAnEnvSourceTheTierCannotRead(t *testing.T) {
	cases := []struct {
		name string
		json string
		want []string
	}{
		{"dotenv in production", `{"production":"dotenv"}`, []string{`"envSource.production"`, "dotenv", "dev"}},
		{"dotenv in preview", `{"preview":"dotenv"}`, []string{`"envSource.preview"`, "dotenv", "dev"}},
		{"builtin in dev", `{"dev":"builtin"}`, []string{`"envSource.dev"`, "builtin", "production and preview"}},
		{"an env source ocel does not know", `{"production":"vault"}`, []string{`"envSource.production"`, `"vault"`, "builtin, exec, infisical"}},
		{"an env source keyed twice", `{"production":{"infisical":{},"exec":{}}}`, []string{`"envSource.production"`, "exec and infisical"}},
		{"an env source keyed by nothing", `{"production":{}}`, []string{`"envSource.production"`, "keyed by nothing"}},
		{"a tier default written as a key", `{"dev":{"dotenv":{}}}`, []string{`"envSource.dev"`, `"dotenv"`, "named alone"}},
		{"infisical named alone", `{"production":"infisical"}`, []string{`"envSource.production"`, `{ "infisical": {`}},
		{"infisical with null options", `{"production":{"infisical":null}}`, []string{`"envSource.production.infisical"`, "an object of options"}},
		{"infisical with no project", `{"production":{"infisical":{"environment":"prod","auth":{"identity":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.project"`}},
		{"infisical with no environment", `{"production":{"infisical":{"project":"p","auth":{"identity":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.environment"`}},
		{"infisical under a path not rooted at /", `{"production":{"infisical":{"project":"p","environment":"prod","path":"web","auth":{"identity":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.path"`, "/"}},
		{"infisical on a host that is no URL", `{"production":{"infisical":{"project":"p","environment":"prod","host":"infisical.example.com","auth":{"identity":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.host"`, "https"}},
		{"a deployed infisical with no auth", `{"production":{"infisical":{"project":"p","environment":"prod"}}}`, []string{`"envSource.production.infisical.auth"`, "machine identity"}},
		{"a dev infisical with auth", `{"dev":{"infisical":{"project":"p","environment":"dev","auth":{"identity":{"identityId":"i"}}}}}`, []string{`"envSource.dev.infisical.auth"`, "INFISICAL_TOKEN"}},
		{"a dev infisical that writes", `{"dev":{"infisical":{"project":"p","environment":"dev","write":"missing"}}}`, []string{`"envSource.dev.infisical.write"`, "only reads"}},
		{"an unknown write policy", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"identity":{"identityId":"i"}},"write":"always"}}}`, []string{`"envSource.production.infisical.write"`, "never, missing, values"}},
		{"auth keyed twice", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"identity":{"identityId":"i"},"universal":{"clientId":{"$env":"ID"},"clientSecret":{"$env":"SECRET"}}}}}}`, []string{`"envSource.production.infisical.auth"`, "universal, identity"}},
		{"auth as text", `{"production":{"infisical":{"project":"p","environment":"prod","auth":"identity"}}}`, []string{`"envSource.production.infisical.auth"`, "universal, identity"}},
		{"auth keyed by a cloud", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"aws":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.auth.aws"`, "universal, identity"}},
		{"cloud identity auth with no identity", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"identity":{}}}}}`, []string{`"envSource.production.infisical.auth.identity.identityId"`}},
		{"universal auth with a literal secret", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"universal":{"clientId":{"$env":"ID"},"clientSecret":"s3cret"}}}}}`, []string{`"envSource.production.infisical.auth.universal.clientSecret"`, `"$env"`}},
		{"universal auth with no client secret", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"universal":{"clientId":{"$env":"ID"}}}}}}`, []string{`"envSource.production.infisical.auth.universal.clientSecret"`}},
		{"exec with no command", `{"dev":{"exec":{"command":[],"format":"json"}}}`, []string{`"envSource.dev.exec.command"`}},
		{"exec with an unknown format", `{"dev":{"exec":{"command":["x"],"format":"yaml"}}}`, []string{`"envSource.dev.exec.format"`, "json, dotenv"}},
		{"an unknown tier", `{"staging":"builtin"}`, []string{`"envSource.staging"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(`{"slug":"acme","envSource":`+c.json+`}`), env(nil))
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
