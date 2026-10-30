package account

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func inventoriedOf(inventoried []pricing.Resource, typ string) []pricing.Resource {
	var out []pricing.Resource
	for _, s := range inventoried {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

func TestTheCoreInventoriesItsBucketsTablesAndLayers(t *testing.T) {
	t.Parallel()

	inventoried, err := Inventory(Namespace("ocel"), environment.TierProduction, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(inventoriedOf(inventoried, "aws_s3_bucket")); got != 4 {
		t.Errorf("buckets = %d, want state, artifacts, edge and cache", got)
	}
	tables := inventoriedOf(inventoried, "aws_dynamodb_table")
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want state and variables", len(tables))
	}
	for _, table := range tables {
		if table.Properties["billing_mode"] != "PAY_PER_REQUEST" {
			t.Errorf("%s billing_mode = %v", table.Name, table.Properties["billing_mode"])
		}
	}
	if got := len(inventoriedOf(inventoried, "aws_lambda_layer_version")); got != 2 {
		t.Errorf("layers = %d, want one per architecture", got)
	}
	if got := len(inventoriedOf(inventoried, "aws_kms_key")); got != 0 {
		t.Errorf("a core without the variables-key part inventoried %d KMS keys", got)
	}
}

func TestEveryPartTogetherInventoriesTheVariablesKeyAndNoFunctionOrQueue(t *testing.T) {
	t.Parallel()

	inventoried, err := Inventory(Namespace("ocel"), environment.TierProduction, []string{provider.VariablesKeyPart, CloudflareEdgePart})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(inventoriedOf(inventoried, "aws_kms_key")); got != 1 {
		t.Errorf("KMS keys = %d, want the variables key", got)
	}
	for _, typ := range []string{"aws_lambda_function", "aws_sqs_queue"} {
		if got := inventoriedOf(inventoried, typ); len(got) != 0 {
			t.Errorf("%s = %v, want none: a deploy adds the functions and queues it needs on first use", typ, got)
		}
	}
}

func TestATemplateFunctionIsInventoriedAsTheTemplateSizesIt(t *testing.T) {
	t.Parallel()

	inventoried, err := inventoryTemplate(cfn.Template{Resources: []cfn.Resource{{
		ID:   "Worker",
		Type: "AWS::Lambda::Function",
		Properties: map[string]any{
			"Runtime":       "nodejs22.x",
			"MemorySize":    1769,
			"Timeout":       30,
			"Architectures": []string{"arm64"},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	functions := inventoriedOf(inventoried, "aws_lambda_function")
	if len(functions) != 1 || functions[0].Name != "Worker" {
		t.Fatalf("functions = %v, want Worker alone", functions)
	}
	properties := functions[0].Properties
	if properties["memory_size"] != float64(1769) || properties["timeout"] != float64(30) || properties["runtime"] != "nodejs22.x" {
		t.Errorf("Worker = %v, want the memory, timeout and runtime its template sets", properties)
	}
	if arch := properties["architectures"].([]any); len(arch) != 1 || arch[0] != "arm64" {
		t.Errorf("Worker architectures = %v, want arm64", properties["architectures"])
	}
}

func TestABroughtVariablesKeyInventoriesNoKey(t *testing.T) {
	t.Parallel()

	inventoried, err := Inventory(Namespace("ocel"), environment.TierPreview, []string{provider.VariablesKeyPart}, WithVariablesKey("arn:aws:kms:us-east-1:1:key/k"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(inventoriedOf(inventoried, "aws_kms_key")); got != 0 {
		t.Errorf("KMS keys = %d, want none for a key the account brought", got)
	}
}
