package bootstrap

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
)

func itemsOf(items []costkit.Item, typ string) []costkit.Item {
	var out []costkit.Item
	for _, s := range items {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

func TestTheCoreInventoriesItsBucketsTablesAndLayers(t *testing.T) {
	t.Parallel()

	items, err := Inventory(Namespace("ocel"), ClassProduction, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(itemsOf(items, "aws_s3_bucket")); got != 3 {
		t.Errorf("buckets = %d, want state, artifact and asset", got)
	}
	tables := itemsOf(items, "aws_dynamodb_table")
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want state and vars", len(tables))
	}
	for _, table := range tables {
		if table.Properties["billing_mode"] != "PAY_PER_REQUEST" {
			t.Errorf("%s billing_mode = %v", table.Name, table.Properties["billing_mode"])
		}
	}
	if got := len(itemsOf(items, "aws_lambda_layer_version")); got != 2 {
		t.Errorf("layers = %d, want one per architecture", got)
	}
	if got := len(itemsOf(items, "aws_kms_key")); got != 0 {
		t.Errorf("a core without the vars-key feature inventories %d KMS keys", got)
	}
}

func TestFeaturesInventoryTheirFunctionsAsTheTemplateSizesThem(t *testing.T) {
	t.Parallel()

	items, err := Inventory(Namespace("ocel"), ClassProduction, []string{FeatureISR, FeatureImageOptimization, FeatureVarsKey, FeatureCloudflareEdge})
	if err != nil {
		t.Fatal(err)
	}
	memory := map[string]float64{}
	for _, fn := range itemsOf(items, "aws_lambda_function") {
		memory[fn.Name] = fn.Properties["memory_size"].(float64)
		if arch := fn.Properties["architectures"].([]any); len(arch) != 1 || arch[0] != "arm64" {
			t.Errorf("%s architectures = %v, want arm64", fn.Name, fn.Properties["architectures"])
		}
	}
	want := map[string]float64{"ImageOptimizer": 1769, "Revalidator": 512, "TagInvalidator": 512, "TagPublisher": 512}
	for name, mb := range want {
		if memory[name] != mb {
			t.Errorf("%s memory = %v, want %v", name, memory[name], mb)
		}
	}
	if len(memory) != len(want) {
		t.Errorf("functions = %v", memory)
	}
	if got := len(itemsOf(items, "aws_kms_key")); got != 1 {
		t.Errorf("KMS keys = %d, want the vars key", got)
	}
	queues := itemsOf(items, "aws_sqs_queue")
	if len(queues) != 4 {
		t.Errorf("queues = %d, want the revalidate queue, its dead letters, and one per invalidator and publisher", len(queues))
	}
}

func TestABroughtVarsKeyInventoriesNoKey(t *testing.T) {
	t.Parallel()

	items, err := Inventory(Namespace("ocel"), ClassPreview, []string{FeatureVarsKey}, WithVarsKey("arn:aws:kms:us-east-1:1:key/k"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(itemsOf(items, "aws_kms_key")); got != 0 {
		t.Errorf("KMS keys = %d, want none for a key the account brought", got)
	}
}
