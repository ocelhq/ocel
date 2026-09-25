package configdoc

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestDecodeEnvSourceKeyedByItsIdentifierPerTier(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","envSource":{
		"production":{"infisical":{"project":"p-1","environment":"prod","path":"/acme","auth":{"universal":{"clientId":{"var":"INFISICAL_CLIENT_ID"},"clientSecret":{"var":"INFISICAL_CLIENT_SECRET"}}},"write":"missing"}},
		"preview":"builtin",
		"dev":{"exec":{"command":["op","inject","{folder}"],"format":"dotenv"}}
	}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	production := doc.EnvSource.Production
	if production == nil || production.ID != "infisical" || production.Infisical == nil {
		t.Fatalf("production = %+v, want infisical", production)
	}
	infisical := production.Infisical
	if infisical.Project != "p-1" || infisical.Environment != "prod" || infisical.Path != "/acme" || infisical.Write != "missing" {
		t.Fatalf("infisical = %+v", infisical)
	}
	if infisical.Auth == nil || infisical.Auth.Universal == nil ||
		infisical.Auth.Universal.ClientID.Var != "INFISICAL_CLIENT_ID" ||
		infisical.Auth.Universal.ClientSecret.Var != "INFISICAL_CLIENT_SECRET" {
		t.Fatalf("auth = %+v", infisical.Auth)
	}
	if doc.EnvSource.Preview == nil || doc.EnvSource.Preview.ID != "builtin" {
		t.Fatalf("preview = %+v, want builtin", doc.EnvSource.Preview)
	}
	dev := doc.EnvSource.Dev
	if dev == nil || dev.ID != "exec" || dev.Exec == nil || !slices.Equal(dev.Exec.Command, []string{"op", "inject", "{folder}"}) || dev.Exec.Format != "dotenv" {
		t.Fatalf("dev = %+v, want exec", dev)
	}
}

func TestDecodeEnvSourceTakesCloudIdentityAuth(t *testing.T) {
	for _, identity := range []string{"aws", "gcp"} {
		doc, err := Decode([]byte(`{"slug":"acme","envSource":{"production":{"infisical":{"project":"p","environment":"prod","auth":{"`+identity+`":{"identityId":"id-1"}}}}}}`), env(nil))
		if err != nil {
			t.Fatalf("decode %s: %v", identity, err)
		}
		auth := doc.EnvSource.Production.Infisical.Auth
		var held *IdentityAuth
		switch identity {
		case "aws":
			held = auth.AWS
		case "gcp":
			held = auth.GCP
		}
		if held == nil || held.IdentityID != "id-1" {
			t.Fatalf("%s auth = %+v", identity, auth)
		}
	}
}

func TestDecodeEnvSourceLeftOffIsEveryTierDefault(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"acme","envSource":{"dev":"dotenv"}}`), env(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.EnvSource.Production != nil || doc.EnvSource.Preview != nil {
		t.Fatalf("envSource = %+v, want production and preview left to their default", doc.EnvSource)
	}
	if doc.EnvSource.Dev == nil || doc.EnvSource.Dev.ID != "dotenv" {
		t.Fatalf("dev = %+v, want dotenv", doc.EnvSource.Dev)
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
		{"a source nobody ships", `{"production":"vault"}`, []string{`"envSource.production"`, `"vault"`, "builtin, infisical, exec"}},
		{"a source keyed twice", `{"production":{"infisical":{},"exec":{}}}`, []string{`"envSource.production"`, "exec and infisical"}},
		{"infisical named alone", `{"production":"infisical"}`, []string{`"envSource.production"`, `{ "infisical": {`}},
		{"infisical with no project", `{"production":{"infisical":{"environment":"prod","auth":{"aws":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.project"`}},
		{"infisical with no environment", `{"production":{"infisical":{"project":"p","auth":{"aws":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.environment"`}},
		{"a deployed infisical with no auth", `{"production":{"infisical":{"project":"p","environment":"prod"}}}`, []string{`"envSource.production.infisical.auth"`, "machine identity"}},
		{"a dev infisical with auth", `{"dev":{"infisical":{"project":"p","environment":"dev","auth":{"aws":{"identityId":"i"}}}}}`, []string{`"envSource.dev.infisical.auth"`, "INFISICAL_TOKEN"}},
		{"an unknown write policy", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"aws":{"identityId":"i"}},"write":"always"}}}`, []string{`"envSource.production.infisical.write"`, "missing, never"}},
		{"auth keyed twice", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"aws":{"identityId":"i"},"gcp":{"identityId":"i"}}}}}`, []string{`"envSource.production.infisical.auth"`, "aws and gcp"}},
		{"universal auth with a literal secret", `{"production":{"infisical":{"project":"p","environment":"prod","auth":{"universal":{"clientId":{"var":"ID"},"clientSecret":"s3cret"}}}}}`, []string{`"envSource.production.infisical.auth.universal.clientSecret"`, "var"}},
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

func TestEnvSourceSchemaNamesWhatEachTierMayRead(t *testing.T) {
	generated, err := Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	var schema struct {
		Properties struct {
			EnvSource struct {
				Properties map[string]struct {
					Title string `json:"title"`
					OneOf []struct {
						Enum       []string                   `json:"enum"`
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"oneOf"`
				} `json:"properties"`
			} `json:"envSource"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(generated, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tiers := schema.Properties.EnvSource.Properties
	for tier, alone := range map[string]string{"production": "builtin", "preview": "builtin", "dev": "dotenv"} {
		shape, held := tiers[tier]
		if !held || len(shape.OneOf) != 2 {
			t.Fatalf("envSource.%s = %+v, want a string or an object", tier, shape)
		}
		if !slices.Equal(shape.OneOf[0].Enum, []string{alone}) {
			t.Errorf("envSource.%s named alone = %v, want only %s", tier, shape.OneOf[0].Enum, alone)
		}
		keyed := make([]string, 0, 2)
		for key := range shape.OneOf[1].Properties {
			keyed = append(keyed, key)
		}
		slices.Sort(keyed)
		if !slices.Equal(keyed, []string{"exec", "infisical"}) {
			t.Errorf("envSource.%s keyed by %v, want exec and infisical", tier, keyed)
		}
	}
}
