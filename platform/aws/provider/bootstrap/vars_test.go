package bootstrap

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type varsTemplate struct {
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

func parseVarsTemplate(t *testing.T, template string) varsTemplate {
	t.Helper()
	var tmpl varsTemplate
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

func varsSubstrates() []struct {
	name     string
	class    string
	template string
} {
	return []struct {
		name     string
		class    string
		template string
	}{
		{"production", ClassProduction, stackTemplate()},
		{"preview", ClassPreview, previewStackTemplate()},
	}
}

func TestVarsTable(t *testing.T) {
	for _, tc := range varsSubstrates() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseVarsTemplate(t, tc.template)

			table, ok := tmpl.Resources["VarsTable"]
			if !ok {
				t.Fatal("template is missing the VarsTable resource")
			}
			if table.Type != "AWS::DynamoDB::Table" {
				t.Errorf("VarsTable Type = %q, want AWS::DynamoDB::Table", table.Type)
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
			if idx.IndexName != VarsTableIndexName {
				t.Errorf("IndexName = %q, want %q", idx.IndexName, VarsTableIndexName)
			}
			if len(idx.KeySchema) != 2 ||
				idx.KeySchema[0].AttributeName != "gsi1pk" || idx.KeySchema[0].KeyType != "HASH" ||
				idx.KeySchema[1].AttributeName != "gsi1sk" || idx.KeySchema[1].KeyType != "RANGE" {
				t.Errorf("index KeySchema = %+v, want gsi1pk HASH + gsi1sk RANGE", idx.KeySchema)
			}
			if idx.Projection.ProjectionType != "KEYS_ONLY" {
				t.Errorf("index ProjectionType = %q, want KEYS_ONLY", idx.Projection.ProjectionType)
			}

			if _, ok := tmpl.Outputs[outputVarsTable]; !ok {
				t.Fatalf("template is missing the %s output", outputVarsTable)
			}
			if tmpl.Outputs[outputVarsTable].Value == tmpl.Outputs[outputStateTable].Value {
				t.Error("the vars table output resolves to the state table; the store must have a table of its own")
			}
		})
	}
}

func TestVarsKey(t *testing.T) {
	aliases := map[string]string{}
	for _, tc := range varsSubstrates() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseVarsTemplate(t, tc.template)

			key, ok := tmpl.Resources["VarsKey"]
			if !ok {
				t.Fatal("template is missing the VarsKey resource")
			}
			if key.Type != "AWS::KMS::Key" {
				t.Errorf("VarsKey Type = %q, want AWS::KMS::Key", key.Type)
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
				tagged = tagged || tag.Key == varsKeyComponentTagKey && tag.Value == varsKeyComponentTagValue
			}
			if !tagged {
				t.Errorf("VarsKey Tags = %+v, want %s=%s, which is what the bootstrap credential policy scopes key lifecycle to", key.Properties.Tags, varsKeyComponentTagKey, varsKeyComponentTagValue)
			}

			alias, ok := tmpl.Resources["VarsKeyAlias"]
			if !ok {
				t.Fatal("template is missing the VarsKeyAlias resource")
			}
			if alias.Type != "AWS::KMS::Alias" {
				t.Errorf("VarsKeyAlias Type = %q, want AWS::KMS::Alias", alias.Type)
			}
			if got, want := alias.Properties.AliasName, varsKeyAliasFor(tc.class); got != want {
				t.Errorf("AliasName = %q, want %q", got, want)
			}
			aliases[tc.class] = alias.Properties.AliasName

			if _, ok := tmpl.Outputs[outputVarsKeyARN]; !ok {
				t.Fatalf("template is missing the %s output", outputVarsKeyARN)
			}
		})
	}
	if aliases[ClassProduction] == aliases[ClassPreview] {
		t.Errorf("both classes alias the key %q; each class must own its own key", aliases[ClassProduction])
	}
}

func TestVarsResourcesAreStackOwned(t *testing.T) {
	for _, tc := range varsSubstrates() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseVarsTemplate(t, tc.template)
			for _, name := range []string{"VarsTable", "VarsKey", "VarsKeyAlias"} {
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
}

func TestVarsDescriptions(t *testing.T) {
	const maxDescriptionLen = 1024

	for _, tc := range varsSubstrates() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseVarsTemplate(t, tc.template)

			described := map[string]string{
				"VarsKey":        tmpl.Resources["VarsKey"].Properties.Description,
				"VarsKeyAlias":   tmpl.Resources["VarsKeyAlias"].Metadata.Description,
				"VarsTable":      tmpl.Resources["VarsTable"].Metadata.Description,
				outputVarsTable:  tmpl.Outputs[outputVarsTable].Description,
				outputVarsKeyARN: tmpl.Outputs[outputVarsKeyARN].Description,
			}
			for name, description := range described {
				if description == "" {
					t.Errorf("%s carries no description; an operator meets it in the console with no context", name)
					continue
				}
				if len(description) > maxDescriptionLen {
					t.Errorf("%s description is %d characters, over the %d CloudFormation accepts", name, len(description), maxDescriptionLen)
				}
				if !strings.Contains(description, " ") || !strings.HasSuffix(strings.TrimSpace(description), ".") {
					t.Errorf("%s description = %q, want a sentence", name, description)
				}
			}

			for _, name := range []string{"VarsKey", "VarsKeyAlias", "VarsTable"} {
				if !strings.Contains(described[name], tc.class) {
					t.Errorf("%s description = %q, want it to name the %s class it belongs to", name, described[name], tc.class)
				}
			}
			if !strings.Contains(described["VarsKey"], "again") {
				t.Errorf("VarsKey description = %q, want it to say what deleting the key costs", described["VarsKey"])
			}
			if !strings.Contains(described["VarsTable"], "again") {
				t.Errorf("VarsTable description = %q, want it to say what deleting the table costs", described["VarsTable"])
			}
		})
	}
}

func TestRunVars(t *testing.T) {
	t.Run("provisions the variable store idempotently", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		standInCloudflare(t, &fakeEdge{kind: "cloudflare"})

		for i := range 2 {
			if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), everything(), nil, nil); err != nil {
				t.Fatalf("Run %d: %v", i+1, err)
			}
		}
		if cfn.creates != 4 {
			t.Errorf("stacks were created %d times across two bootstraps, want one create each for core and its three features", cfn.creates)
		}

		tmpl := parseVarsTemplate(t, cfn.template(StackName))
		for _, name := range []string{"VarsTable", "VarsKey", "VarsKeyAlias"} {
			if _, ok := tmpl.Resources[name]; !ok {
				t.Errorf("the account's stack no longer declares %s after a re-run", name)
			}
		}
	})

	t.Run("upgrades a pre store account to the variable store", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		seed := preStoreTemplate(t)
		cfn.seed(StackName, seed)

		before := parseVarsTemplate(t, seed)
		for _, name := range []string{"VarsTable", "VarsKey", "VarsKeyAlias"} {
			if _, ok := before.Resources[name]; ok {
				t.Fatalf("the seeded account already declares %s; it is not a pre-store account", name)
			}
		}
		for _, name := range []string{outputVarsTable, outputVarsKeyARN} {
			if _, ok := before.Outputs[name]; ok {
				t.Fatalf("the seeded account already outputs %s; it is not a pre-store account", name)
			}
		}

		standInCloudflare(t, &fakeEdge{kind: "cloudflare"})
		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), Request{}, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}

		if cfn.creates != 0 {
			t.Errorf("the upgrade created %d stacks; a live account must be updated, not replaced", cfn.creates)
		}
		if cfn.updates != 1 {
			t.Errorf("the account's stack was updated %d times, want 1", cfn.updates)
		}

		after := parseVarsTemplate(t, cfn.template(StackName))
		for _, name := range []string{"VarsTable", "VarsKey", "VarsKeyAlias"} {
			if _, ok := after.Resources[name]; !ok {
				t.Errorf("the upgrade did not add %s", name)
			}
		}
		for _, name := range []string{outputVarsTable, outputVarsKeyARN} {
			if _, ok := after.Outputs[name]; !ok {
				t.Errorf("the upgrade did not add the %s output", name)
			}
		}
		if _, ok := after.Resources["StateBucket"]; !ok {
			t.Error("the upgrade dropped the state bucket")
		}
	})
}

func preStoreTemplate(t *testing.T) string {
	t.Helper()
	tmpl := stackTemplate()
	for _, block := range []string{varsResources(ClassProduction), varsOutputs()} {
		if block == "" || !strings.Contains(tmpl, block) {
			t.Fatalf("cannot derive a pre-store template: the current one has no\n%s", block)
		}
		tmpl = strings.Replace(tmpl, block, "", 1)
	}
	return tmpl
}

func TestCheckDeployedVars(t *testing.T) {
	t.Run("parses vars outputs", func(t *testing.T) {
		api := stubDescriber{StackName: outputs(map[string]string{
			outputVarsTable:  "vars-abc",
			outputVarsKeyARN: "arn:aws:kms:eu-west-1:123456789012:key/abcd",
		})}

		got, err := CheckDeployed(context.Background(), api)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if got.VarsTable != "vars-abc" {
			t.Errorf("VarsTable = %q, want vars-abc", got.VarsTable)
		}
		if got.VarsKeyARN != "arn:aws:kms:eu-west-1:123456789012:key/abcd" {
			t.Errorf("VarsKeyARN = %q, want the key ARN from the stack output", got.VarsKeyARN)
		}
	})
}
