package bootstrap

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/cloud/edge"
)

// varsTemplate is the subset of a rendered template needed to assert the
// variable store: its own table, its own class key, and whether either is
// retained past a stack delete.
type varsTemplate struct {
	Resources map[string]struct {
		Type           string `yaml:"Type"`
		DeletionPolicy string `yaml:"DeletionPolicy"`
		Properties     struct {
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
			KeyPolicy         struct {
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
		Value string `yaml:"Value"`
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

// varsSubstrates is the pair of rendered substrate templates every vars
// assertion runs against: the store is provisioned per class, so a property
// that holds for one class only is a property that does not hold.
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
		{"production", ClassProduction, stackTemplate(edge.TrustExternal)},
		{"preview", ClassPreview, previewStackTemplate(edge.TrustExternal)},
	}
}

// TestVarsTable asserts both substrate templates provision the variable store's
// own table, separate from the state table, under the same opaque pk/sk pair
// and with the single secondary index the reverse reference lookup reads.
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

			// One index, and only one: every consumer operation maps to a point
			// read or a prefix query, so a second access path is a signal that
			// something is about to scan.
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
			// The reverse lookup answers "what points at this value", which is a
			// list of coordinates; projecting the values themselves would put a
			// second copy of every referenced ciphertext in the index.
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

// TestVarsKey asserts each substrate provisions its own encryption key, aliased
// by class — the isolation property varsResources argues for.
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

			// The key policy delegates to IAM rather than enumerating grantees:
			// the principals that may decrypt are function execution roles the
			// deploy creates long after bootstrap, so a key policy that named
			// them would have to be rewritten on every deploy.
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
	// Both substrates can be bootstrapped into one account, so a shared alias
	// would be a stack collision — and, worse, a shared key.
	if aliases[ClassProduction] == aliases[ClassPreview] {
		t.Errorf("both classes alias the key %q; each class must own its own key", aliases[ClassProduction])
	}
}

// TestVarsResourcesAreStackOwned proves destroying the account-global bootstrap
// removes what it created: neither the table nor the key survives its stack.
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

// TestRun_ProvisionsTheVariableStoreIdempotently proves a re-run leaves the
// account holding one variable store, not a second one: the store rides the
// same stack upsert everything else does, so re-running bootstrap converges
// rather than duplicating.
func TestRun_ProvisionsTheVariableStoreIdempotently(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	for i := range 2 {
		if err := Run(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
	}
	if cfn.creates != 1 {
		t.Errorf("stack was created %d times across two bootstraps, want 1", cfn.creates)
	}

	tmpl := parseVarsTemplate(t, cfn.templates[StackName])
	for _, name := range []string{"VarsTable", "VarsKey", "VarsKeyAlias"} {
		if _, ok := tmpl.Resources[name]; !ok {
			t.Errorf("the account's stack no longer declares %s after a re-run", name)
		}
	}
}

// preStoreTemplate derives the stack body as it stood before the variable store
// by removing the store from the current template. Deriving it rather than
// pinning a copy keeps the seed honest: if the store ever stops being a separable
// block, this fails loudly instead of upgrading from a fiction.
func preStoreTemplate(t *testing.T) string {
	t.Helper()
	tmpl := stackTemplate(edge.TrustExternal)
	for _, block := range []string{varsResources(ClassProduction), varsOutputs()} {
		if block == "" || !strings.Contains(tmpl, block) {
			t.Fatalf("cannot derive a pre-store template: the current one has no\n%s", block)
		}
		tmpl = strings.Replace(tmpl, block, "", 1)
	}
	return tmpl
}

// TestRun_UpgradesAPreStoreAccountToTheVariableStore proves the version bump is
// something an existing account can actually converge on: a stack raised before
// the store re-runs bootstrap and gains the table, key and alias plus their
// outputs — updated in place, never replaced.
func TestRun_UpgradesAPreStoreAccountToTheVariableStore(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	seed := preStoreTemplate(t)
	cfn.templates[StackName] = seed
	cfn.statuses[StackName] = cfntypes.StackStatusCreateComplete

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

	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}
	if err := Run(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if cfn.creates != 0 {
		t.Errorf("the upgrade created %d stacks; a live account must be updated, not replaced", cfn.creates)
	}
	if cfn.updates != 1 {
		t.Errorf("the account's stack was updated %d times, want 1", cfn.updates)
	}

	after := parseVarsTemplate(t, cfn.templates[StackName])
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
	if got, want := after.Outputs[outputVersion].Value, strconv.Itoa(RequiredBootstrapVersion); got != want {
		t.Errorf("upgraded stack reports version %q, want %q", got, want)
	}

	if _, ok := after.Resources["StateBucket"]; !ok {
		t.Error("the upgrade dropped the state bucket")
	}
}

// TestCheckDeployed_ParsesVarsOutputs proves the discovery path surfaces the
// store's coordinates, which is how everything downstream reaches it: the
// provider never hardcodes a table name or a key id.
func TestCheckDeployed_ParsesVarsOutputs(t *testing.T) {
	api := stubDescriber{out: &cloudformation.DescribeStacksOutput{
		Stacks: []cfntypes.Stack{{
			Outputs: []cfntypes.Output{
				{OutputKey: aws.String(outputVarsTable), OutputValue: aws.String("vars-abc")},
				{OutputKey: aws.String(outputVarsKeyARN), OutputValue: aws.String("arn:aws:kms:eu-west-1:123456789012:key/abcd")},
			},
		}},
	}}

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
}
