package configdoc

import (
	"encoding/json"
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

func TestOptionsSchemaNamesTheProvider(t *testing.T) {
	type options struct {
		Region string `json:"region,omitempty" doc:"The region to deploy into."`
	}
	generated, err := OptionsSchema("acme", options{})
	if err != nil {
		t.Fatalf("options schema: %v", err)
	}
	var variant struct {
		Properties struct {
			Name struct {
				Const string `json:"const"`
			} `json:"name"`
			Options struct {
				Properties map[string]struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"properties"`
				AdditionalProperties bool `json:"additionalProperties"`
			} `json:"options"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(generated, &variant); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if variant.Properties.Name.Const != "acme" {
		t.Fatalf("name const = %q", variant.Properties.Name.Const)
	}
	region := variant.Properties.Options.Properties["region"]
	if region.Type != "string" || region.Description != "The region to deploy into." {
		t.Fatalf("region = %+v", region)
	}
	if variant.Properties.Options.AdditionalProperties {
		t.Fatal("options accept keys the provider does not declare")
	}
}

func TestOptionsSchemaRefusesAnUnnamedProvider(t *testing.T) {
	type options struct {
		Region string `json:"region,omitempty"`
	}
	if _, err := OptionsSchema("", options{}); err == nil {
		t.Fatal("options schema for an unnamed provider = nil error, want a refusal")
	}
}

type patterned struct {
	Key string `json:"key,omitempty" pattern:"^arn:aws:kms:"`
}

func TestAPatternReachesTheGeneratedSchema(t *testing.T) {
	generated, err := OptionsSchema("aws", patterned{})
	if err != nil {
		t.Fatalf("options schema: %v", err)
	}
	if !strings.Contains(string(generated), `"pattern": "^arn:aws:kms:"`) {
		t.Errorf("schema = %s, want the pattern carried into it", generated)
	}
}

func TestAValueIsCheckedAgainstItsPattern(t *testing.T) {
	held := map[string]any{"key": "arn:aws:kms:eu-west-1:111122223333:key/abcd"}
	if err := Check("provider.options", patterned{}, held); err != nil {
		t.Fatalf("a key that matches its pattern was refused: %v", err)
	}
	err := Check("provider.options", patterned{}, map[string]any{"key": "abcd"})
	if err == nil {
		t.Fatal("a key that does not match its pattern was taken")
	}
	if !strings.Contains(err.Error(), "provider.options.key") || !strings.Contains(err.Error(), "^arn:aws:kms:") {
		t.Errorf("error = %q, want it to name the key and the pattern", err)
	}
}
