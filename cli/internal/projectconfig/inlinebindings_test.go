package projectconfig

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func resolveJSON(t *testing.T, config string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), config)
	return Resolve(context.Background(), dir, "")
}

func mustResolveJSON(t *testing.T, config string) *Config {
	t.Helper()
	cfg, err := resolveJSON(t, config)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return cfg
}

func TestAnInlineBindingServesTheTiersItNames(t *testing.T) {
	cfg := mustResolveJSON(t, `{"slug":"shop","bindings":{"postgres":{
		"analytics":"@warehouse",
		"orders":{"url":{"$env":"ORDERS_DATABASE_URL"}},
		"billing":{"production":{"host":"db.example.com","database":"billing","username":"app","password":{"$env":"BILLING_PASSWORD"}}}
	}}}`)

	production := cfg.BindingsFor(TierProduction)
	preview := cfg.BindingsFor(TierPreview)
	names := func(bound []TierBinding) []string {
		out := make([]string, 0, len(bound))
		for _, b := range bound {
			out = append(out, b.Name)
		}
		return out
	}
	if got, want := names(production), []string{"analytics", "billing", "orders"}; !slices.Equal(got, want) {
		t.Errorf("production binds %v, want %v", got, want)
	}
	if got, want := names(preview), []string{"analytics", "orders"}; !slices.Equal(got, want) {
		t.Errorf("preview binds %v, want %v: billing names production alone, so previews provision their own", got, want)
	}
	for _, b := range production {
		switch b.Name {
		case "analytics":
			if b.External != "warehouse" || b.Inline != nil {
				t.Errorf("analytics = %+v, want the published record", b)
			}
		case "billing":
			if b.Inline == nil || b.Inline.Postgres == nil {
				t.Fatalf("billing = %+v, want the inline record", b)
			}
			want := PostgresInline{
				Host:     Value{Literal: "db.example.com"},
				Database: Value{Literal: "billing"},
				Username: Value{Literal: "app"},
				Password: "BILLING_PASSWORD",
			}
			if !reflect.DeepEqual(*b.Inline.Postgres, want) {
				t.Errorf("billing = %+v, want %+v", *b.Inline.Postgres, want)
			}
			if b.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
				t.Errorf("billing type = %v", b.Type)
			}
		}
	}
}

func TestAnInlineBindingNamesTheVariablesItReads(t *testing.T) {
	cfg := mustResolveJSON(t, `{"slug":"shop","bindings":{"postgres":{"orders":{
		"host":{"$env":"ORDERS_HOST"},"database":"orders","username":{"$env":"ORDERS_USER"},
		"password":{"$env":"ORDERS_PASSWORD"},"tls":{"mode":"verify-full","ca":{"$env":"ORDERS_CA"}}
	}}}}`)
	bound := cfg.BindingsFor(TierProduction)
	if len(bound) != 1 || bound[0].Inline == nil {
		t.Fatalf("bound = %+v", bound)
	}
	got := bound[0].Inline.Variables()
	want := []string{"ORDERS_CA", "ORDERS_HOST", "ORDERS_PASSWORD", "ORDERS_USER"}
	if !slices.Equal(got, want) {
		t.Errorf("Variables() = %v, want %v", got, want)
	}
}

func TestAnInlineBindingIsRefusedWhereItCannotConnect(t *testing.T) {
	cases := []struct {
		name    string
		binding string
		want    []string
	}{
		{
			name:    "a host form missing its password",
			binding: `{"host":"db","database":"d","username":"u"}`,
			want:    []string{"bindings.postgres.orders", "password"},
		},
		{
			name:    "a host form missing its database",
			binding: `{"host":"db","username":"u","password":{"$env":"P"}}`,
			want:    []string{"bindings.postgres.orders", "database"},
		},
		{
			name:    "an empty host",
			binding: `{"host":" ","database":"d","username":"u","password":{"$env":"P"}}`,
			want:    []string{"bindings.postgres.orders.host"},
		},
		{
			name:    "a port no server listens on",
			binding: `{"host":"db","port":70000,"database":"d","username":"u","password":{"$env":"P"}}`,
			want:    []string{"bindings.postgres.orders.port", "70000"},
		},
		{
			name:    "a variable no ocel variable can be named",
			binding: `{"url":{"$env":"orders-url"}}`,
			want:    []string{"bindings.postgres.orders.url", `"orders-url"`},
		},
		{
			name:    "a variable in the prefix ocel writes",
			binding: `{"url":{"$env":"OCEL_ORDERS_URL"}}`,
			want:    []string{"bindings.postgres.orders.url", "OCEL_"},
		},
		{
			name:    "an empty tier",
			binding: `{"production":{}}`,
			want:    []string{"bindings.postgres.orders.production"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveJSON(t, `{"slug":"shop","bindings":{"postgres":{"orders":`+c.binding+`}}}`)
			if err == nil {
				t.Fatalf("resolved %s", c.binding)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %s", err, want)
				}
			}
		})
	}
}

func TestAnInlineBucketNamesItsStoreAndTheVariablesItReads(t *testing.T) {
	cfg := mustResolveJSON(t, `{"slug":"shop","bindings":{"bucket":{"uploads":{
		"endpoint":"https://abc.r2.cloudflarestorage.com","region":"auto","bucket":{"$env":"UPLOADS_BUCKET"},
		"prefix":"uploads/","accessKeyId":{"$env":"R2_KEY"},"secretAccessKey":{"$env":"R2_SECRET"},
		"publicBaseUrl":"https://cdn.acme.com"
	}}}}`)
	bound := cfg.BindingsFor(TierPreview)
	if len(bound) != 1 || bound[0].Inline == nil || bound[0].Inline.Bucket == nil {
		t.Fatalf("bound = %+v, want the inline bucket", bound)
	}
	want := BucketInline{
		Endpoint:        Value{Literal: "https://abc.r2.cloudflarestorage.com"},
		Region:          Value{Literal: "auto"},
		Bucket:          Value{Variable: "UPLOADS_BUCKET"},
		Prefix:          Value{Literal: "uploads/"},
		AccessKeyID:     "R2_KEY",
		SecretAccessKey: "R2_SECRET",
		PublicBaseURL:   Value{Literal: "https://cdn.acme.com"},
	}
	if !reflect.DeepEqual(*bound[0].Inline.Bucket, want) {
		t.Errorf("bucket = %+v, want %+v", *bound[0].Inline.Bucket, want)
	}
	if got := bound[0].Inline.Variables(); !slices.Equal(got, []string{"R2_KEY", "R2_SECRET", "UPLOADS_BUCKET"}) {
		t.Errorf("Variables() = %v", got)
	}
}

func TestAnInlineBucketIsRefusedWhereNoStoreCouldBeReached(t *testing.T) {
	for name, binding := range map[string]string{
		"no secret":       `{"endpoint":"https://s3.example.com","region":"auto","bucket":"acme","accessKeyId":{"$env":"K"}}`,
		"no endpoint":     `{"region":"auto","bucket":"acme","accessKeyId":{"$env":"K"},"secretAccessKey":{"$env":"S"}}`,
		"a bare endpoint": `{"endpoint":"s3.example.com","region":"auto","bucket":"acme","accessKeyId":{"$env":"K"},"secretAccessKey":{"$env":"S"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveJSON(t, `{"slug":"shop","bindings":{"bucket":{"uploads":`+binding+`}}}`)
			if err == nil || !strings.Contains(err.Error(), "bindings.bucket.uploads") {
				t.Fatalf("resolve = %v, want the binding refused by path", err)
			}
		})
	}
}

func TestAnInlineBindingIsBoundUnderANameNoPublisherHolds(t *testing.T) {
	_, err := resolveJSON(t, `{"slug":"shop","bindings":{"postgres":{"orders":"@`+naming.InlineRecordPrefix+`postgres.orders"}}}`)
	if err == nil {
		t.Fatal("resolved a binding to a record name reserved for inline bindings")
	}
	if !strings.Contains(err.Error(), naming.InlineRecordPrefix) {
		t.Errorf("error %q does not name the reserved prefix", err)
	}
}
