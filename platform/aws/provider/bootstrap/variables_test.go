package bootstrap

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"

	"gopkg.in/yaml.v3"
)

type variablesTemplate struct {
	Resources map[string]struct {
		Type           string `yaml:"Type"`
		DeletionPolicy string `yaml:"DeletionPolicy"`
		Metadata       struct {
			Description string `yaml:"Description"`
		} `yaml:"Metadata"`
		Properties struct {
			Description          string `yaml:"Description"`
			BillingMode          string `yaml:"BillingMode"`
			AttributeDefinitions []struct {
				AttributeName string `yaml:"AttributeName"`
				AttributeType string `yaml:"AttributeType"`
			} `yaml:"AttributeDefinitions"`
			KeySchema []struct {
				AttributeName string `yaml:"AttributeName"`
				KeyType       string `yaml:"KeyType"`
			} `yaml:"KeySchema"`
			GlobalSecondaryIndexes []struct {
				IndexName string `yaml:"IndexName"`
				KeySchema []struct {
					AttributeName string `yaml:"AttributeName"`
					KeyType       string `yaml:"KeyType"`
				} `yaml:"KeySchema"`
				Projection struct {
					ProjectionType string `yaml:"ProjectionType"`
				} `yaml:"Projection"`
			} `yaml:"GlobalSecondaryIndexes"`

			EnableKeyRotation bool   `yaml:"EnableKeyRotation"`
			AliasName         string `yaml:"AliasName"`
			TargetKeyId       string `yaml:"TargetKeyId"`
			Tags              []struct {
				Key   string `yaml:"Key"`
				Value string `yaml:"Value"`
			} `yaml:"Tags"`
			KeyPolicy struct {
				Statement []struct {
					Effect    string `yaml:"Effect"`
					Principal struct {
						AWS string `yaml:"AWS"`
					} `yaml:"Principal"`
					Action   any    `yaml:"Action"`
					Resource string `yaml:"Resource"`
				} `yaml:"Statement"`
			} `yaml:"KeyPolicy"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Description string `yaml:"Description"`
		Value       string `yaml:"Value"`
	} `yaml:"Outputs"`
}

func parseVariablesTemplate(t *testing.T, template string) variablesTemplate {
	t.Helper()
	var tmpl variablesTemplate
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

func variablesBootstraps() []struct {
	name     string
	tier     environment.Tier
	template string
} {
	return []struct {
		name     string
		tier     environment.Tier
		template string
	}{
		{"production", environment.TierProduction, coreStackTemplate(defaultNamespace, environment.TierProduction, "")},
		{"preview", environment.TierPreview, coreStackTemplate(defaultNamespace, environment.TierPreview, "")},
	}
}

func variablesKeyBootstraps() []struct {
	name     string
	tier     environment.Tier
	template string
} {
	return []struct {
		name     string
		tier     environment.Tier
		template string
	}{
		{"production", environment.TierProduction, featureTemplate(provider.FeatureVariablesKey, environment.TierProduction)},
		{"preview", environment.TierPreview, featureTemplate(provider.FeatureVariablesKey, environment.TierPreview)},
	}
}

func TestTheVariablesTableIsAPayPerRequestDynamoDBTable(t *testing.T) {
	for _, tc := range variablesBootstraps() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseVariablesTemplate(t, tc.template)

			table, ok := tmpl.Resources["VariablesTable"]
			if !ok {
				t.Fatal("template is missing the VariablesTable resource")
			}
			if table.Type != "AWS::DynamoDB::Table" {
				t.Errorf("VariablesTable Type = %q, want AWS::DynamoDB::Table", table.Type)
			}
			if table.Properties.BillingMode != "PAY_PER_REQUEST" {
				t.Errorf("BillingMode = %q, want PAY_PER_REQUEST", table.Properties.BillingMode)
			}

			attrs := table.Properties.AttributeDefinitions
			if len(attrs) != 4 ||
				attrs[0].AttributeName != "pk" || attrs[0].AttributeType != "S" ||
				attrs[1].AttributeName != "sk" || attrs[1].AttributeType != "S" ||
				attrs[2].AttributeName != "gsi1pk" || attrs[2].AttributeType != "S" ||
				attrs[3].AttributeName != "gsi1sk" || attrs[3].AttributeType != "S" {
				t.Errorf("AttributeDefinitions = %+v, want pk/sk and gsi1pk/gsi1sk, all (S)", attrs)
			}
			keys := table.Properties.KeySchema
			if len(keys) != 2 ||
				keys[0].AttributeName != "pk" || keys[0].KeyType != "HASH" ||
				keys[1].AttributeName != "sk" || keys[1].KeyType != "RANGE" {
				t.Errorf("KeySchema = %+v, want pk HASH + sk RANGE", keys)
			}

			idxs := table.Properties.GlobalSecondaryIndexes
			if len(idxs) != 1 {
				t.Fatalf("GlobalSecondaryIndexes = %+v, want exactly the reference index", idxs)
			}
			idx := idxs[0]
			if idx.IndexName != VariablesTableIndexName {
				t.Errorf("IndexName = %q, want %q", idx.IndexName, VariablesTableIndexName)
			}
			if len(idx.KeySchema) != 2 ||
				idx.KeySchema[0].AttributeName != "gsi1pk" || idx.KeySchema[0].KeyType != "HASH" ||
				idx.KeySchema[1].AttributeName != "gsi1sk" || idx.KeySchema[1].KeyType != "RANGE" {
				t.Errorf("index KeySchema = %+v, want gsi1pk HASH + gsi1sk RANGE", idx.KeySchema)
			}
			if idx.Projection.ProjectionType != "KEYS_ONLY" {
				t.Errorf("index ProjectionType = %q, want KEYS_ONLY", idx.Projection.ProjectionType)
			}

			if _, ok := tmpl.Outputs[outputVariablesTable]; !ok {
				t.Fatalf("template is missing the %s output", outputVariablesTable)
			}
			if tmpl.Outputs[outputVariablesTable].Value == tmpl.Outputs[outputStateTable].Value {
				t.Error("the variables table output resolves to the state table; the store must have a table of its own")
			}
		})
	}
}

func TestTheVariablesKeyRotatesAndGrantsOnlyThroughIAM(t *testing.T) {
	aliases := map[environment.Tier]string{}
	for _, tc := range variablesKeyBootstraps() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseVariablesTemplate(t, tc.template)

			key, ok := tmpl.Resources["VariablesKey"]
			if !ok {
				t.Fatal("template is missing the VariablesKey resource")
			}
			if key.Type != "AWS::KMS::Key" {
				t.Errorf("VariablesKey Type = %q, want AWS::KMS::Key", key.Type)
			}
			if !key.Properties.EnableKeyRotation {
				t.Error("EnableKeyRotation = false, want true: the key outlives every value encrypted under it")
			}

			stmts := key.Properties.KeyPolicy.Statement
			if len(stmts) != 1 {
				t.Fatalf("KeyPolicy statements = %d, want exactly the one enabling IAM policies", len(stmts))
			}
			st := stmts[0]
			if st.Effect != "Allow" || st.Principal.AWS != "arn:aws:iam::${AWS::AccountId}:root" {
				t.Errorf("KeyPolicy statement = %+v, want Allow for the account root principal", st)
			}
			if !hasAction(st.Action, "kms:*") {
				t.Errorf("KeyPolicy Action = %v, want kms:*", st.Action)
			}

			tagged := false
			for _, tag := range key.Properties.Tags {
				tagged = tagged || tag.Key == VariablesKeyComponentTagKey && tag.Value == VariablesKeyComponentTagValue
			}
			if !tagged {
				t.Errorf("VariablesKey Tags = %+v, want %s=%s, which is what the bootstrap credential policy scopes key lifecycle to", key.Properties.Tags, VariablesKeyComponentTagKey, VariablesKeyComponentTagValue)
			}

			alias, ok := tmpl.Resources["VariablesKeyAlias"]
			if !ok {
				t.Fatal("template is missing the VariablesKeyAlias resource")
			}
			if alias.Type != "AWS::KMS::Alias" {
				t.Errorf("VariablesKeyAlias Type = %q, want AWS::KMS::Alias", alias.Type)
			}
			if got, want := alias.Properties.AliasName, defaultNamespace.variablesKeyAliasFor(tc.tier); got != want {
				t.Errorf("AliasName = %q, want %q", got, want)
			}
			aliases[tc.tier] = alias.Properties.AliasName

			if _, ok := tmpl.Outputs[outputVariablesKeyARN]; !ok {
				t.Fatalf("template is missing the %s output", outputVariablesKeyARN)
			}
		})
	}
	if aliases[environment.TierProduction] == aliases[environment.TierPreview] {
		t.Errorf("both tiers alias the key %q; each tier must own its own key", aliases[environment.TierProduction])
	}
}

func TestVariablesResourcesAreStackOwned(t *testing.T) {
	for _, tc := range variablesBootstraps() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseVariablesTemplate(t, tc.template)
			for _, name := range []string{"VariablesTable"} {
				res, ok := tmpl.Resources[name]
				if !ok {
					t.Errorf("template is missing the %s resource", name)
					continue
				}
				if res.DeletionPolicy != "" {
					t.Errorf("%s DeletionPolicy = %q, want none so a stack delete removes it", name, res.DeletionPolicy)
				}
			}
		})
	}
	for _, tc := range variablesKeyBootstraps() {
		t.Run("key/"+tc.name, func(t *testing.T) {
			tmpl := parseVariablesTemplate(t, tc.template)
			for _, name := range []string{"VariablesKey", "VariablesKeyAlias"} {
				res, ok := tmpl.Resources[name]
				if !ok {
					t.Errorf("the variables-key stack is missing the %s resource", name)
					continue
				}
				if res.DeletionPolicy != "" {
					t.Errorf("%s DeletionPolicy = %q, want none so a stack delete removes it", name, res.DeletionPolicy)
				}
			}
		})
	}
}

func TestEveryVariablesResourceIsDescribedForItsTier(t *testing.T) {
	const maxDescriptionLen = 1024

	for _, tc := range variablesBootstraps() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseVariablesTemplate(t, tc.template)

			key := parseVariablesTemplate(t, featureTemplate(provider.FeatureVariablesKey, tc.tier))
			described := map[string]string{
				"VariablesKey":        key.Resources["VariablesKey"].Properties.Description,
				"VariablesKeyAlias":   key.Resources["VariablesKeyAlias"].Metadata.Description,
				"VariablesTable":      tmpl.Resources["VariablesTable"].Metadata.Description,
				outputVariablesTable:  tmpl.Outputs[outputVariablesTable].Description,
				outputVariablesKeyARN: key.Outputs[outputVariablesKeyARN].Description,
			}
			for name, description := range described {
				if description == "" {
					t.Errorf("%s has no description; an operator meets it in the console with no context", name)
					continue
				}
				if len(description) > maxDescriptionLen {
					t.Errorf("%s description is %d characters, over the %d CloudFormation accepts", name, len(description), maxDescriptionLen)
				}
				if !strings.Contains(description, " ") || !strings.HasSuffix(strings.TrimSpace(description), ".") {
					t.Errorf("%s description = %q, want a sentence", name, description)
				}
			}

			for _, name := range []string{"VariablesKey", "VariablesKeyAlias", "VariablesTable"} {
				if !strings.Contains(described[name], string(tc.tier)) {
					t.Errorf("%s description = %q, want it to name the %s tier it belongs to", name, described[name], tc.tier)
				}
			}
			if !strings.Contains(described["VariablesKey"], "again") {
				t.Errorf("VariablesKey description = %q, want it to say what deleting the key costs", described["VariablesKey"])
			}
			if !strings.Contains(described["VariablesTable"], "again") {
				t.Errorf("VariablesTable description = %q, want it to say what deleting the table costs", described["VariablesTable"])
			}
		})
	}
}

func TestTheCoreStackHoldsTheVariablesTableAndTheKeyStackTheKey(t *testing.T) {
	t.Run("the core stack contains the table and no key", func(t *testing.T) {
		for _, tc := range variablesBootstraps() {
			t.Run(tc.name, func(t *testing.T) {
				tmpl := parseVariablesTemplate(t, tc.template)
				if _, ok := tmpl.Resources["VariablesTable"]; !ok {
					t.Error("the core stack no longer declares VariablesTable")
				}
				for _, name := range []string{"VariablesKey", "VariablesKeyAlias"} {
					if _, ok := tmpl.Resources[name]; ok {
						t.Errorf("the core stack declares %s, and a key bills whether or not a value is ever set", name)
					}
				}
			})
		}
	})

	t.Run("a run that asks for the key raises a stack of its own", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		req := Request{Features: []string{provider.FeatureVariablesKey}}
		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, req, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}

		tmpl := parseVariablesTemplate(t, stacks.template(defaultNamespace.FeatureStackName(provider.FeatureVariablesKey, environment.TierProduction)))
		for _, name := range []string{"VariablesKey", "VariablesKeyAlias"} {
			if _, ok := tmpl.Resources[name]; !ok {
				t.Errorf("the variables-key stack does not declare %s", name)
			}
		}
		if _, ok := tmpl.Outputs[outputVariablesKeyARN]; !ok {
			t.Errorf("the variables-key stack does not output %s, so nothing records the key it made", outputVariablesKeyARN)
		}
	})

	t.Run("a run that does not ask for the key makes none", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}

		stack := defaultNamespace.FeatureStackName(provider.FeatureVariablesKey, environment.TierProduction)
		if slices.Contains(stacks.stacks(), stack) {
			t.Errorf("%s exists after a run that never asked for it; bootstrap creates nothing that bills while idle", stack)
		}
	})
}

func TestCheckDeployedReadsTheVariablesOutputs(t *testing.T) {
	t.Run("parses variables outputs", func(t *testing.T) {
		api := stubStacksAPI{coreStackName: outputs(map[string]string{
			outputVariablesTable:  "variables-abc",
			outputVariablesKeyARN: "arn:aws:kms:eu-west-1:123456789012:key/abcd",
		})}

		got, err := CheckDeployed(context.Background(), api, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if got.VariablesTable != "variables-abc" {
			t.Errorf("VariablesTable = %q, want variables-abc", got.VariablesTable)
		}
		if got.VariablesKeyARN != "arn:aws:kms:eu-west-1:123456789012:key/abcd" {
			t.Errorf("VariablesKeyARN = %q, want the key ARN from the stack output", got.VariablesKeyARN)
		}
	})
}
