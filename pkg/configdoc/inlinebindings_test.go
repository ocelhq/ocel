package configdoc

import (
	"strings"
	"testing"
)

func TestDecodeKeepsEachFormABindingTakes(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"shop","bindings":{"postgres":{
		"analytics":"@warehouse",
		"orders":{"url":{"$env":"ORDERS_DATABASE_URL"}},
		"billing":{"production":{"host":"db.${REGION}.example.com","database":"billing","username":"app","password":{"$env":"BILLING_PASSWORD"}}}
	}}}`), env(map[string]string{"REGION": "eu"}))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	named := doc.Bindings["postgres"]
	if got := named["analytics"]; got.Published != "@warehouse" || got.Production != nil || got.Preview != nil {
		t.Errorf("analytics = %+v, want the published record alone", got)
	}
	orders := named["orders"]
	if orders.Published != "" || orders.Production == nil || orders.Preview == nil {
		t.Fatalf("orders = %+v, want one inline record serving both tiers", orders)
	}
	if orders.Production.Postgres.URL == nil || orders.Production.Postgres.URL.Env != "ORDERS_DATABASE_URL" {
		t.Errorf("orders.url = %+v, want the variable it names", orders.Production.Postgres.URL)
	}
	billing := named["billing"]
	if billing.Preview != nil {
		t.Errorf("billing.preview = %+v, want preview left to be provisioned", billing.Preview)
	}
	production := billing.Production
	if production == nil || production.Postgres.Host == nil || production.Postgres.Host.Literal != "db.eu.example.com" {
		t.Fatalf("billing.production = %+v, want the host interpolated from the shell", production)
	}
	if production.Postgres.Password == nil || production.Postgres.Password.Env != "BILLING_PASSWORD" {
		t.Errorf("billing.password = %+v", production.Postgres.Password)
	}
}

func TestCheckRefusesAnInlineBindingOutOfShape(t *testing.T) {
	cases := []struct {
		name    string
		binding any
		want    []string
	}{
		{
			name:    "a secret written as text",
			binding: map[string]any{"host": "db", "database": "d", "username": "u", "password": "hunter2"},
			want:    []string{`"bindings.postgres.orders.password"`, `{ "$env": "NAME" }`},
		},
		{
			name:    "a url written as text",
			binding: map[string]any{"url": "postgres://u:p@db/d"},
			want:    []string{`"bindings.postgres.orders.url"`, `{ "$env": "NAME" }`},
		},
		{
			name:    "a url beside the fields it replaces",
			binding: map[string]any{"url": map[string]any{"$env": "URL"}, "host": "db"},
			want:    []string{`"bindings.postgres.orders"`, "url", "host"},
		},
		{
			name:    "a field postgres does not take",
			binding: map[string]any{"host": "db", "schema": "public"},
			want:    []string{`"bindings.postgres.orders.schema"`},
		},
		{
			name:    "a tier that does not exist",
			binding: map[string]any{"production": map[string]any{"url": map[string]any{"$env": "URL"}}, "staging": map[string]any{}},
			want:    []string{`"bindings.postgres.orders.staging"`},
		},
		{
			name:    "a tls mode postgres does not verify",
			binding: map[string]any{"host": "db", "tls": map[string]any{"mode": "prefer"}},
			want:    []string{`"bindings.postgres.orders.tls.mode"`, "require", "verify-full"},
		},
		{
			name:    "a variable reference carrying a second key",
			binding: map[string]any{"url": map[string]any{"$env": "URL", "default": "x"}},
			want:    []string{`"bindings.postgres.orders.url.default"`},
		},
		{
			name:    "a port written as text",
			binding: map[string]any{"host": "db", "port": "5432"},
			want:    []string{`"bindings.postgres.orders.port"`, "number"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check("", Document{}, map[string]any{
				"slug":     "shop",
				"bindings": map[string]any{"postgres": map[string]any{"orders": c.binding}},
			})
			if err == nil {
				t.Fatalf("Check = nil, want %v refused", c.binding)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Check = %v, want it to say %s", err, want)
				}
			}
		})
	}
}

func TestDecodeKeepsABucketRecordInline(t *testing.T) {
	doc, err := Decode([]byte(`{"slug":"shop","bindings":{"bucket":{"uploads":{
		"endpoint":"https://${CF_ACCOUNT_ID}.r2.cloudflarestorage.com","region":"auto","bucket":{"$env":"UPLOADS_BUCKET"},
		"prefix":"uploads/","pathStyle":true,
		"accessKeyId":{"$env":"R2_ACCESS_KEY_ID"},"secretAccessKey":{"$env":"R2_SECRET_ACCESS_KEY"},
		"publicBaseUrl":"https://cdn.acme.com"
	}}}}`), env(map[string]string{"CF_ACCOUNT_ID": "abc"}))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	uploads := doc.Bindings["bucket"]["uploads"]
	if uploads.Production == nil || uploads.Production != uploads.Preview || uploads.Production.Bucket == nil {
		t.Fatalf("uploads = %+v, want one inline bucket record serving both tiers", uploads)
	}
	record := uploads.Production.Bucket
	if record.Endpoint.Literal != "https://abc.r2.cloudflarestorage.com" || record.Bucket.Ref.Env != "UPLOADS_BUCKET" || !record.PathStyle {
		t.Errorf("record = %+v, want the endpoint interpolated and the bucket read from its variable", record)
	}
	if record.SecretAccessKey.Env != "R2_SECRET_ACCESS_KEY" || record.PublicBaseURL.Literal != "https://cdn.acme.com" {
		t.Errorf("record = %+v", record)
	}
}

func TestCheckRefusesABucketSecretWrittenAsText(t *testing.T) {
	err := Check("", Document{}, map[string]any{
		"slug": "shop",
		"bindings": map[string]any{"bucket": map[string]any{"uploads": map[string]any{
			"endpoint": "https://s3.example.com", "region": "auto", "bucket": "acme",
			"accessKeyId": map[string]any{"$env": "KEY"}, "secretAccessKey": "hunter2",
		}}},
	})
	if err == nil || !strings.Contains(err.Error(), `"bindings.bucket.uploads.secretAccessKey"`) || !strings.Contains(err.Error(), `{ "$env": "NAME" }`) {
		t.Fatalf("Check = %v, want the secret refused as text", err)
	}
}

func TestBindingsSchemaTakesAPostgresRecordInline(t *testing.T) {
	shape := bindingsSchema(t)
	postgres := shape["properties"].(map[string]any)["postgres"].(map[string]any)
	values := postgres["additionalProperties"].(map[string]any)
	forms, ok := values["oneOf"].([]any)
	if !ok || len(forms) != 3 {
		t.Fatalf("bindings.postgres values = %v, want a published name, an inline record, or an inline record per tier", values)
	}
	published := forms[0].(map[string]any)
	if published["type"] != "string" || published["pattern"] != `^@\S` {
		t.Errorf("first form = %v, want the published name", published)
	}
	tiered := forms[2].(map[string]any)
	tiers := tiered["properties"].(map[string]any)
	for _, tier := range []string{"production", "preview"} {
		if _, ok := tiers[tier]; !ok {
			t.Errorf("tiered form = %v, want a %s key", tiered, tier)
		}
	}
	if tiered["additionalProperties"] != false {
		t.Errorf("tiered form = %v, want no other key", tiered)
	}
}
