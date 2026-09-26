package bootstrap

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func shapedOf(shaped []costkit.Shaped, typ string) []costkit.Shaped {
	var out []costkit.Shaped
	for _, s := range shaped {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

func TestTheCoreShapesItsBucketsTablesAndLayers(t *testing.T) {
	t.Parallel()

	shaped, err := Shape(Namespace("ocel"), ClassProduction, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(shapedOf(shaped, "aws_s3_bucket")); got != 3 {
		t.Errorf("buckets = %d, want state, artifact and asset", got)
	}
	tables := shapedOf(shaped, "aws_dynamodb_table")
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want state and vars", len(tables))
	}
	for _, table := range tables {
		if table.Properties["billing_mode"] != "PAY_PER_REQUEST" {
			t.Errorf("%s billing_mode = %v", table.Name, table.Properties["billing_mode"])
		}
	}
	if got := len(shapedOf(shaped, "aws_lambda_layer_version")); got != 2 {
		t.Errorf("layers = %d, want one per architecture", got)
	}
	if got := len(shapedOf(shaped, "aws_kms_key")); got != 0 {
		t.Errorf("a core without the vars-key feature shaped %d KMS keys", got)
	}
}

func TestFeaturesShapeTheirFunctionsAsTheTemplateSizesThem(t *testing.T) {
	t.Parallel()

	shaped, err := Shape(Namespace("ocel"), ClassProduction, []string{FeatureISR, FeatureImageOptimization, provider.FeatureVarsKey, FeatureCloudflareEdge})
	if err != nil {
		t.Fatal(err)
	}
	memory := map[string]float64{}
	for _, fn := range shapedOf(shaped, "aws_lambda_function") {
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
	if got := len(shapedOf(shaped, "aws_kms_key")); got != 1 {
		t.Errorf("KMS keys = %d, want the vars key", got)
	}
	queues := shapedOf(shaped, "aws_sqs_queue")
	if len(queues) != 4 {
		t.Errorf("queues = %d, want the revalidate queue, its dead letters, and one per invalidator and publisher", len(queues))
	}
}

func TestABroughtVarsKeyShapesNoKey(t *testing.T) {
	t.Parallel()

	shaped, err := Shape(Namespace("ocel"), ClassPreview, []string{provider.FeatureVarsKey}, WithVarsKey("arn:aws:kms:us-east-1:1:key/k"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(shapedOf(shaped, "aws_kms_key")); got != 0 {
		t.Errorf("KMS keys = %d, want none for a key the account brought", got)
	}
}
