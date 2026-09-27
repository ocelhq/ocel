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
	infisical := doc.EnvSource.Production.Infisical
	if infisical == nil || infisical.Project != "p-1" || infisical.Environment != "prod" || infisical.Path != "/acme" || infisical.Write != "missing" {
		t.Fatalf("production infisical = %+v", infisical)
	}
	if infisical.Auth == nil || infisical.Auth.Universal == nil ||
		infisical.Auth.Universal.ClientID.Env != "INFISICAL_CLIENT_ID" ||
		infisical.Auth.Universal.ClientSecret.Env != "INFISICAL_CLIENT_SECRET" {
		t.Fatalf("auth = %+v", infisical.Auth)
	}
	preview := doc.EnvSource.Preview
	if preview == nil || preview.Infisical != nil || preview.Exec != nil {
		t.Fatalf("preview = %+v, want builtin named alone", preview)
	}
	dev := doc.EnvSource.Dev
	if dev == nil || dev.Exec == nil || !slices.Equal(dev.Exec.Command, []string{"op", "inject", "{folder}"}) || dev.Exec.Format != "dotenv" {
		t.Fatalf("dev = %+v, want exec", dev)
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

func TestEachTierReadsTheEnvSourceItNamesWithItsDefaultsFilledIn(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","envSource":{
		"production":{"infisical":{"project":"p-1","environment":"prod","auth":{"universal":{"clientId":{"$env":"ID"},"clientSecret":{"$env":"SECRET"}}}}},
		"preview":{"infisical":{"project":"p-1","environment":"staging","path":"acme/","host":"https://infisical.example.com/","write":"missing","auth":{"identity":{"identityId":"ident"}}}},
		"dev":{"exec":{"command":["op","run","{folder}"],"format":"json"}}
	}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := envsource.Tiers{
		Production: envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
			Project:     "p-1",
			Environment: "prod",
			Path:        "/",
			Host:        "https://app.infisical.com",
			Write:       envsource.WriteNever,
			Auth:        envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVariable: "ID", ClientSecretVariable: "SECRET"},
		}},
		Preview: envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
			Project:     "p-1",
			Environment: "staging",
			Path:        "/acme",
			Host:        "https://infisical.example.com",
			Write:       envsource.WriteMissing,
			Auth:        envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "ident"},
		}},
		Dev: envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{
			Command: []string{"op", "run", "{folder}"},
			Format:  envsource.FormatJSON,
		}},
	}
	if got := doc.EnvSource.Tiers(); !reflect.DeepEqual(got, want) {
		t.Fatalf("tiers =\n%+v\nwant\n%+v", got, want)
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
		if !slices.Equal(keyed, []string{"infisical", "exec"}) {
			t.Errorf("envSource.%s keyed by %v, want infisical and exec", tier, keyed)
		}
	}
}

func TestEachEnvSourceTypeIsDocumentedAsItselfNotAsAFieldThatUsesIt(t *testing.T) {
	docs := namedTypeDocs(t)
	for title, own := range map[string]string{
		"EnvSourceDescriptor":    EnvSourceDescriptor{}.Doc(),
		"DevEnvSourceDescriptor": DevEnvSourceDescriptor{}.Doc(),
		"InfisicalOptions":       InfisicalOptions{}.Doc(),
		"InfisicalAuth":          InfisicalAuth{}.Doc(),
		"UniversalAuth":          UniversalAuth{}.Doc(),
		"IdentityAuth":           IdentityAuth{}.Doc(),
		"ExecOptions":            ExecOptions{}.Doc(),
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
		{"an env source ocel does not know", `{"production":"vault"}`, []string{`"envSource.production"`, `"vault"`, "builtin, infisical, exec"}},
		{"an env source keyed twice", `{"production":{"infisical":{},"exec":{}}}`, []string{`"envSource.production"`, "exec and infisical"}},
		{"an env source keyed by nothing", `{"production":{}}`, []string{`"envSource.production"`, "keyed by nothing"}},
		{"a tier default written as a key", `{"dev":{"dotenv":{}}}`, []string{`"envSource.dev"`, `"dotenv"`, "named alone"}},
		{"infisical named alone", `{"production":"infisical"}`, []string{`"envSource.production"`, `{ "infisical": {`}},
		{"infisical with null options", `{"production":{"infisical":null}}`, []string{`"envSource.production.infisical"`, "an object of options"}},
		{"infisical with no project", `{"production":{"infisical":{"environment":"prod","auth":{"identity":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.project"`}},
		{"infisical with no environment", `{"production":{"infisical":{"project":"p","auth":{"identity":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.environment"`}},
		{"a deployed infisical with no auth", `{"production":{"infisical":{"project":"p","environment":"prod"}}}`, []string{`"envSource.production.infisical.auth"`, "machine identity"}},
		{"a dev infisical with auth", `{"dev":{"infisical":{"project":"p","environment":"dev","auth":{"identity":{"identityId":"i"}}}}}`, []string{`"envSource.dev.infisical.auth"`, "INFISICAL_TOKEN"}},
		{"a dev infisical that writes", `{"dev":{"infisical":{"project":"p","environment":"dev","write":"missing"}}}`, []string{`"envSource.dev.infisical.write"`, "only reads"}},
		{"an unknown write policy", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"identity":{"identityId":"i"}},"write":"always"}}}`, []string{`"envSource.production.infisical.write"`, "missing, never"}},
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
