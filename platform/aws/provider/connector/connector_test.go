package connector

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const defaultNamespace = bootstrap.Namespace(providerkit.DefaultNamespace)

var sealingKeys = []string{
	"arn:aws:kms:eu-west-1:111122223333:key/production-key",
	"arn:aws:kms:eu-west-1:111122223333:key/preview-key",
}

func TestTheStackStandsApartFromEveryBootstrapStack(t *testing.T) {
	t.Parallel()

	named := StackName(defaultNamespace)
	production, err := defaultNamespace.StackNameFor(bootstrap.ClassProduction)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := defaultNamespace.StackNameFor(bootstrap.ClassPreview)
	if err != nil {
		t.Fatal(err)
	}
	for _, held := range []string{production, preview, defaultNamespace.CoreStackName()} {
		if named == held || strings.HasPrefix(named, held) {
			t.Errorf("the connector stack is named %q, which a bootstrap stack named %q would be read as part of", named, held)
		}
	}
}

func TestTheTierGrantsNothingADeployWouldNeed(t *testing.T) {
	t.Parallel()

	held := map[string]bool{}
	for _, statement := range tier(defaultNamespace, sealingKeys) {
		for _, action := range statement.Actions {
			held[action] = true
		}
	}
	for _, action := range []string{
		"cloudformation:CreateChangeSet",
		"cloudformation:CreateStack",
		"cloudformation:UpdateStack",
		"iam:CreateRole",
		"iam:PassRole",
		"lambda:CreateFunction",
		"lambda:UpdateFunctionCode",
		"s3:PutObject",
		"kms:CreateKey",
		"dynamodb:CreateTable",
	} {
		if held[action] {
			t.Errorf("the connector tier grants %s, and a connector reads and writes variables rather than deploying", action)
		}
	}
	for _, action := range []string{
		"dynamodb:GetItem",
		"dynamodb:Query",
		"dynamodb:TransactWriteItems",
		"kms:Decrypt",
		"kms:Encrypt",
		"cloudformation:DescribeStacks",
		"sts:GetCallerIdentity",
	} {
		if !held[action] {
			t.Errorf("the connector tier withholds %s, which the ports the connector serves call", action)
		}
	}
}

func TestTheTemplateCarriesTheCodeTheBucketStagesAndTheConfigItRuns(t *testing.T) {
	t.Parallel()

	at := payloads.At("staged-bucket", codePrefix, payloads.Of([]byte("connector")))
	rendered, err := templateFor(defaultNamespace, at, "0.9.9",
		[]byte(`{"console":"https://console.example.com"}`), sealingKeys)
	if err != nil {
		t.Fatalf("templateFor: %v", err)
	}

	var read struct {
		Resources map[string]struct {
			Type       string         `json:"Type"`
			Properties map[string]any `json:"Properties"`
		} `json:"Resources"`
		Outputs map[string]struct {
			Value any `json:"Value"`
		} `json:"Outputs"`
	}
	if err := json.Unmarshal([]byte(rendered), &read); err != nil {
		t.Fatalf("the connector template is not a template CloudFormation could read: %v", err)
	}

	for _, want := range []string{"CodeBucket", "ConnectorRole", "ConnectorFunction", "ConnectorUrl", "ConnectorUrlReachable"} {
		if _, held := read.Resources[want]; !held {
			t.Errorf("the connector template carries no %s", want)
		}
	}
	if held := read.Resources["CodeBucket"].Type; held != "AWS::S3::Bucket" {
		t.Errorf("the connector stages its code in a %s, want a bucket of its own rather than the bootstrap's", held)
	}

	function := read.Resources["ConnectorFunction"].Properties
	code, _ := function["Code"].(map[string]any)
	if code["S3Key"] != at.Key {
		t.Errorf("the function reads its code at %v, want the digest-keyed object this install staged (%s)", code["S3Key"], at.Key)
	}
	if _, staged := code["S3Bucket"].(map[string]any)["Ref"]; !staged {
		t.Errorf("the function reads its code out of %v, want the bucket this stack owns", code["S3Bucket"])
	}
	if arch, _ := function["Architectures"].([]any); len(arch) != 1 || arch[0] != Arch {
		t.Errorf("the function runs on %v, want %s alone", function["Architectures"], Arch)
	}
	env, _ := function["Environment"].(map[string]any)["Variables"].(map[string]any)
	if env["OCEL_CONNECTOR_CONFIG_JSON"] != `{"console":"https://console.example.com"}` {
		t.Errorf("the function carries %v as its config, and a function has no filesystem to read one off", env["OCEL_CONNECTOR_CONFIG_JSON"])
	}

	url := read.Resources["ConnectorUrl"].Properties
	if url["AuthType"] != "NONE" {
		t.Errorf("the function url authenticates with %v, and the console's own token is what the connector checks", url["AuthType"])
	}
	if read.Outputs[outputVersion].Value != "0.9.9" {
		t.Errorf("the stack reports version %v, want the release this install carried", read.Outputs[outputVersion].Value)
	}
}

func TestTheKeyGrantNamesTheKeysThisNamespaceSealedUnderAndNoOther(t *testing.T) {
	t.Parallel()

	at := payloads.At("staged-bucket", codePrefix, payloads.Of([]byte("connector")))
	rendered, err := templateFor(defaultNamespace, at, "0.9.9",
		[]byte(`{"console":"https://console.example.com"}`), sealingKeys)
	if err != nil {
		t.Fatalf("templateFor: %v", err)
	}

	if strings.Contains(rendered, bootstrap.AnyKeyARN) {
		t.Errorf("the connector policy names %s, so a compromised connector would decrypt every namespace's variables in the account", bootstrap.AnyKeyARN)
	}
	for _, key := range sealingKeys {
		if !strings.Contains(rendered, key) {
			t.Errorf("the connector policy names no %s, and that is a key this namespace seals variables under", key)
		}
	}

	for _, statement := range tier(defaultNamespace, sealingKeys) {
		if !slices.Contains(statement.Actions, "kms:Decrypt") {
			continue
		}
		if !slices.Equal(statement.Resources, sealingKeys) {
			t.Errorf("the key grant reaches %v, want %v alone", statement.Resources, sealingKeys)
		}
		if statement.Condition == nil {
			t.Error("the key grant carries no tag condition, and the tag is what keeps a renamed key out")
		}
	}
}

type noStacks struct{}

func (noStacks) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput,
	...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	return &cloudformation.DescribeStacksOutput{}, nil
}

func TestAnAccountWithNoBootstrappedKeyRefusesTheInstall(t *testing.T) {
	t.Parallel()

	_, err := varsKeys(context.Background(), noStacks{}, defaultNamespace)
	if err == nil {
		t.Fatal("an account with neither class bootstrapped rendered a policy, and it would name no key to scope the grant to")
	}
	if !strings.Contains(err.Error(), "ocel connector add") {
		t.Errorf("the refusal reads %q, and it should say what finishes the install", err)
	}
}

func TestATemplateNamingNoKeyIsRefused(t *testing.T) {
	t.Parallel()

	at := payloads.At("staged-bucket", codePrefix, payloads.Of([]byte("connector")))
	if _, err := templateFor(defaultNamespace, at, "0.9.9",
		[]byte(`{"console":"https://console.example.com"}`), nil); err == nil {
		t.Error("a template naming no key rendered, and an empty Resources list is a policy CloudFormation refuses or a grant that reaches nothing")
	}
}

func TestTheSameConnectorBytesStageUnderTheSameKey(t *testing.T) {
	t.Parallel()

	first, err := zipped([]byte("connector"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := zipped([]byte("connector"))
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != again.SHA256 {
		t.Error("the same connector bytes archived to two digests, so a re-run would update a stack nothing changed in")
	}
	moved, err := zipped([]byte("connector-next"))
	if err != nil {
		t.Fatal(err)
	}
	if moved.SHA256 == first.SHA256 {
		t.Error("two different connectors archived to one digest, so an upgrade would leave the old code running")
	}

	read, err := zip.NewReader(bytes.NewReader(first.Bytes), int64(len(first.Bytes)))
	if err != nil {
		t.Fatalf("the archive is not a zip a function could be built from: %v", err)
	}
	named := make([]string, 0, len(read.File))
	for _, entry := range read.File {
		named = append(named, entry.Name)
	}
	if !slices.Equal(named, []string{handler}) {
		t.Errorf("the archive holds %v, and provided.al2023 runs the entry named %s", named, handler)
	}
}

func TestTheCodeTemplateOpensNothingButTheBucket(t *testing.T) {
	t.Parallel()

	var read struct {
		Resources map[string]json.RawMessage `json:"Resources"`
	}
	if err := json.Unmarshal([]byte(codeTemplate()), &read); err != nil {
		t.Fatalf("the connector code template is not a template CloudFormation could read: %v", err)
	}
	if len(read.Resources) != 1 || read.Resources["CodeBucket"] == nil {
		t.Errorf("the first pass opens %v, and it opens only the bucket the code is staged in before anything reads it",
			slices.Sorted(mapKeys(read.Resources)))
	}
}

func mapKeys[V any](held map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range held {
			if !yield(key) {
				return
			}
		}
	}
}

func TestReplacingTheCodeBucketIsRefused(t *testing.T) {
	t.Parallel()

	err := reviewing(defaultNamespace, nil)(StackName(defaultNamespace), []cfntypes.ResourceChange{{
		LogicalResourceId: aws.String("CodeBucket"),
		ResourceType:      aws.String("AWS::S3::Bucket"),
		Action:            cfntypes.ChangeActionModify,
		Replacement:       cfntypes.ReplacementTrue,
	}})
	if err == nil {
		t.Fatal("reviewing() = nil, want a template that replaces the bucket the connector's code sits in refused")
	}
	if !strings.Contains(err.Error(), "CodeBucket") {
		t.Errorf("reviewing() = %q, want it to name what would be replaced", err)
	}
}

func TestAnInPlaceUpdateOfTheFunctionPasses(t *testing.T) {
	t.Parallel()

	err := reviewing(defaultNamespace, nil)(StackName(defaultNamespace), []cfntypes.ResourceChange{{
		LogicalResourceId: aws.String("Function"),
		ResourceType:      aws.String("AWS::Lambda::Function"),
		Action:            cfntypes.ChangeActionModify,
		Replacement:       cfntypes.ReplacementFalse,
	}})
	if err != nil {
		t.Fatalf("reviewing() = %v, want a new connector release written in place", err)
	}
}
