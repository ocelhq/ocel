package bootstrap

import (
	"context"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"gopkg.in/yaml.v3"
)

// parsedTemplate is the subset of the rendered bootstrap template the tests
// assert against.
type parsedTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			BillingMode          string `yaml:"BillingMode"`
			AttributeDefinitions []struct {
				AttributeName string `yaml:"AttributeName"`
				AttributeType string `yaml:"AttributeType"`
			} `yaml:"AttributeDefinitions"`
			KeySchema []struct {
				AttributeName string `yaml:"AttributeName"`
				KeyType       string `yaml:"KeyType"`
			} `yaml:"KeySchema"`
			TimeToLiveSpecification struct {
				AttributeName string `yaml:"AttributeName"`
				Enabled       bool   `yaml:"Enabled"`
			} `yaml:"TimeToLiveSpecification"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Value string `yaml:"Value"`
	} `yaml:"Outputs"`
}

func parseTemplate(t *testing.T) parsedTemplate {
	t.Helper()
	var tmpl parsedTemplate
	if err := yaml.Unmarshal([]byte(stackTemplate()), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

// TestStackTemplate_SessionsTable asserts the bootstrap template provisions the
// account-global sessions table with exactly the schema the runtime expects:
// partition key session_id (S), no sort key, and a TTL on expires_at.
func TestStackTemplate_SessionsTable(t *testing.T) {
	tmpl := parseTemplate(t)

	table, ok := tmpl.Resources["SessionsTable"]
	if !ok {
		t.Fatal("template is missing the SessionsTable resource")
	}
	if table.Type != "AWS::DynamoDB::Table" {
		t.Errorf("SessionsTable Type = %q, want AWS::DynamoDB::Table", table.Type)
	}
	if table.Properties.BillingMode != "PAY_PER_REQUEST" {
		t.Errorf("BillingMode = %q, want PAY_PER_REQUEST", table.Properties.BillingMode)
	}

	if got := table.Properties.AttributeDefinitions; len(got) != 1 || got[0].AttributeName != "session_id" || got[0].AttributeType != "S" {
		t.Errorf("AttributeDefinitions = %+v, want single session_id (S)", got)
	}
	if got := table.Properties.KeySchema; len(got) != 1 || got[0].AttributeName != "session_id" || got[0].KeyType != "HASH" {
		t.Errorf("KeySchema = %+v, want single session_id HASH (no sort key)", got)
	}

	ttl := table.Properties.TimeToLiveSpecification
	if ttl.AttributeName != "expires_at" || !ttl.Enabled {
		t.Errorf("TimeToLiveSpecification = %+v, want expires_at enabled", ttl)
	}

	if _, ok := tmpl.Outputs[outputSessionTable]; !ok {
		t.Errorf("template is missing the %s output", outputSessionTable)
	}
}

// TestStackTemplate_VersionOutput proves the deployed version output is
// single-sourced from RequiredBootstrapVersion, so the two never drift.
func TestStackTemplate_VersionOutput(t *testing.T) {
	tmpl := parseTemplate(t)
	if got := tmpl.Outputs[outputVersion].Value; got != strconv.Itoa(RequiredBootstrapVersion) {
		t.Errorf("%s output = %q, want %d", outputVersion, got, RequiredBootstrapVersion)
	}
}

// stubDescriber returns a fixed DescribeStacks response.
type stubDescriber struct {
	out *cloudformation.DescribeStacksOutput
}

func (s stubDescriber) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	return s.out, nil
}

// TestCheckDeployed_ParsesOutputs proves the discovery path surfaces the
// session table name, state bucket, version, and class marker that later
// deploys and the class guard depend on.
func TestCheckDeployed_ParsesOutputs(t *testing.T) {
	api := stubDescriber{out: &cloudformation.DescribeStacksOutput{
		Stacks: []cfntypes.Stack{{
			Outputs: []cfntypes.Output{
				{OutputKey: aws.String(outputStateBucket), OutputValue: aws.String("bucket-123")},
				{OutputKey: aws.String(outputSessionTable), OutputValue: aws.String("sessions-abc")},
				{OutputKey: aws.String(outputVersion), OutputValue: aws.String("2")},
				{OutputKey: aws.String(outputInfraClass), OutputValue: aws.String(ClassProduction)},
			},
		}},
	}}

	got, err := CheckDeployed(context.Background(), api)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	want := Deployed{Present: true, Version: 2, StateBucket: "bucket-123", SessionTable: "sessions-abc", Class: ClassProduction}
	if got != want {
		t.Errorf("CheckDeployed = %+v, want %+v", got, want)
	}
}

// TestCheckDeployed_ReadsPreviewClassMarker proves the class stamped on a
// substrate is read back, so the class guard can verify a command acts on the
// right one.
func TestCheckDeployed_ReadsPreviewClassMarker(t *testing.T) {
	api := stubDescriber{out: &cloudformation.DescribeStacksOutput{
		Stacks: []cfntypes.Stack{{
			Outputs: []cfntypes.Output{
				{OutputKey: aws.String(outputInfraClass), OutputValue: aws.String(ClassPreview)},
			},
		}},
	}}

	got, err := CheckDeployed(context.Background(), api)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	if !got.Present || got.Class != ClassPreview {
		t.Errorf("CheckDeployed = %+v, want Present with Class %q", got, ClassPreview)
	}
}

// TestPreviewStackTemplate_StampsPreviewClass proves the preview template stamps
// the preview class marker so CheckDeployedPreview surfaces it.
func TestPreviewStackTemplate_StampsPreviewClass(t *testing.T) {
	var tmpl parsedTemplate
	if err := yaml.Unmarshal([]byte(previewStackTemplate()), &tmpl); err != nil {
		t.Fatalf("preview template is not valid YAML: %v", err)
	}
	if got := tmpl.Outputs[outputInfraClass].Value; got != ClassPreview {
		t.Errorf("%s output = %q, want %q", outputInfraClass, got, ClassPreview)
	}
}
