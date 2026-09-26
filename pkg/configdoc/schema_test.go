package configdoc

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
)

func TestCoreSchemaIsCommitted(t *testing.T) {
	generated, err := Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	schematest.AssertCommitted(t, schematest.CoreSchemaFile, generated)
}

func TestCoreSchemaDescribesTheDocument(t *testing.T) {
	generated, err := Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(generated, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"slug", "provider", "edge", "dns", "apps", "$schema"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("the schema has no %q property", key)
		}
	}
	if len(schema.Required) != 1 || schema.Required[0] != "slug" {
		t.Fatalf("required = %v, want only slug", schema.Required)
	}
}

func TestProviderSchemaIncludesTheOptionsAndWhatTheProviderFrontsAndWritesWith(t *testing.T) {
	type options struct {
		Region string `json:"region,omitempty" doc:"The region to deploy into."`
	}
	generated, err := ProviderSchema("acme", options{}, []string{"relay", "direct"}, []string{"acme-dns"})
	if err != nil {
		t.Fatalf("provider schema: %v", err)
	}
	var fragment struct {
		ID      string `json:"id"`
		Options struct {
			Title      string `json:"title"`
			Properties map[string]struct {
				Type        string `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
			AdditionalProperties bool `json:"additionalProperties"`
		} `json:"options"`
		Edges []string `json:"edges"`
		DNS   []string `json:"dns"`
	}
	if err := json.Unmarshal(generated, &fragment); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if fragment.ID != "acme" {
		t.Fatalf("id = %q", fragment.ID)
	}
	if fragment.Options.Title != "AcmeProviderOptions" {
		t.Errorf("options title = %q, want AcmeProviderOptions", fragment.Options.Title)
	}
	region := fragment.Options.Properties["region"]
	if region.Type != "string" || region.Description != "The region to deploy into." {
		t.Fatalf("region = %+v", region)
	}
	if fragment.Options.AdditionalProperties {
		t.Fatal("options accept keys the provider does not declare")
	}
	if !slices.Equal(fragment.Edges, []string{"direct", "relay"}) {
		t.Errorf("edges = %v, want the provider's edges in order", fragment.Edges)
	}
	if !slices.Equal(fragment.DNS, []string{"acme-dns"}) {
		t.Errorf("dns = %v", fragment.DNS)
	}
}

func TestProviderSchemaRefusesAnUnidentifiedProvider(t *testing.T) {
	type options struct {
		Region string `json:"region,omitempty"`
	}
	if _, err := ProviderSchema[string, string]("", options{}, nil, nil); err == nil {
		t.Fatal("provider schema for an unidentified provider = nil error, want a refusal")
	}
}

type patterned struct {
	Key string `json:"key,omitempty" pattern:"^arn:aws:kms:"`
}

func TestAPatternReachesTheGeneratedSchema(t *testing.T) {
	generated, err := ProviderSchema[string, string]("aws", patterned{}, nil, nil)
	if err != nil {
		t.Fatalf("options schema: %v", err)
	}
	if !strings.Contains(string(generated), `"pattern": "^arn:aws:kms:"`) {
		t.Errorf("schema = %s, want the pattern copied into it", generated)
	}
	tolerated, err := json.Marshal(interpolationPattern)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), `"pattern": `+string(tolerated)) {
		t.Errorf("schema = %s, want an unresolved interpolation to pass it", generated)
	}
}

func TestAValueIsCheckedAgainstItsPattern(t *testing.T) {
	options := map[string]any{"key": "arn:aws:kms:eu-west-1:111122223333:key/abcd"}
	if err := Check("provider.aws", patterned{}, options); err != nil {
		t.Fatalf("a key that matches its pattern was refused: %v", err)
	}
	err := Check("provider.aws", patterned{}, map[string]any{"key": "abcd"})
	if err == nil {
		t.Fatal("a key that does not match its pattern was taken")
	}
	if !strings.Contains(err.Error(), "provider.aws.key") || !strings.Contains(err.Error(), "^arn:aws:kms:") {
		t.Errorf("error = %q, want it to name the key and the pattern", err)
	}
}
