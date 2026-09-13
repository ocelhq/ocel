package bootstrap

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type ShapeOption func(*featureInputs)

func WithVarsKey(arn string) ShapeOption {
	return func(in *featureInputs) { in.varsKey = arn }
}

const shapedArtifactBucket = "artifacts"

var shapedTypes = map[string]shapedType{
	"AWS::S3::Bucket":                 {token: "aws_s3_bucket"},
	"AWS::DynamoDB::Table":            {token: "aws_dynamodb_table", properties: dynamoProperties},
	"AWS::KMS::Key":                   {token: "aws_kms_key"},
	"AWS::Lambda::Function":           {token: "aws_lambda_function", properties: lambdaProperties},
	"AWS::Lambda::LayerVersion":       {token: "aws_lambda_layer_version"},
	"AWS::Lambda::Url":                {token: "aws_lambda_function_url"},
	"AWS::Lambda::EventSourceMapping": {token: "aws_lambda_event_source_mapping"},
	"AWS::Logs::LogGroup":             {token: "aws_cloudwatch_log_group", properties: logGroupProperties},
	"AWS::SQS::Queue":                 {token: "aws_sqs_queue", properties: queueProperties},
	"AWS::CloudFront::Function":       {token: "aws_cloudfront_function"},
	"AWS::CloudFront::KeyValueStore":  {token: "aws_cloudfront_key_value_store"},
	"AWS::ApiGateway::RestApi":        {token: "aws_api_gateway_rest_api"},
}

type shapedType struct {
	token      string
	properties func(map[string]any) map[string]any
}

func Shape(ns Namespace, class string, features []string, options ...ShapeOption) ([]costkit.Shaped, error) {
	in := featureInputs{ns: ns, class: class, artifactBucket: shapedArtifactBucket}
	for _, option := range options {
		option(&in)
	}
	in.alongside = FeatureSet{}
	for _, name := range features {
		in.alongside[name] = true
	}
	bodies := []string{coreStackTemplate(ns, class), runtimeLayerTemplate(ns, class, shapedLayerPlacements())}
	for _, name := range features {
		f, known := featureNamed(name)
		if !known {
			return nil, providerkit.Refuse(providerkit.CodeInvalid, "this provider has no bootstrap feature named %q", name)
		}
		bodies = append(bodies, f.planned(in).body)
	}
	var shaped []costkit.Shaped
	for _, body := range bodies {
		shaped = append(shaped, shapeTemplate(body)...)
	}
	return shaped, nil
}

func shapedLayerPlacements() map[string]payloads.Placement {
	placed := map[string]payloads.Placement{}
	for _, arch := range runtimeArches() {
		placed[arch] = payloads.Placement{Bucket: shapedArtifactBucket, Key: "runtime/" + arch, SHA256: strings.Repeat("0", 64)}
	}
	return placed
}

func shapeTemplate(body string) []costkit.Shaped {
	resources := templateSection(body, "Resources")
	if resources == nil {
		return nil
	}
	var out []costkit.Shaped
	for i := 0; i+1 < len(resources.Content); i += 2 {
		kind := mappingValue(resources.Content[i+1], "Type")
		if kind == nil {
			continue
		}
		typ, priced := shapedTypes[kind.Value]
		if !priced {
			continue
		}
		properties := map[string]any{}
		if raw, ok := generic(mappingValue(resources.Content[i+1], "Properties")).(map[string]any); ok && typ.properties != nil {
			properties = typ.properties(raw)
		}
		out = append(out, costkit.Shaped{Name: resources.Content[i].Value, Type: typ.token, Properties: properties})
	}
	return out
}

func generic(node *yaml.Node) any {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.MappingNode:
		out := make(map[string]any, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			out[node.Content[i].Value] = generic(node.Content[i+1])
		}
		return out
	case yaml.SequenceNode:
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			out = append(out, generic(item))
		}
		return out
	case yaml.ScalarNode:
		if number, err := strconv.ParseFloat(node.Value, 64); err == nil && node.Tag != "!!str" {
			return number
		}
		if node.Value == "true" || node.Value == "false" {
			return node.Value == "true"
		}
		return node.Value
	}
	return nil
}

func lambdaProperties(raw map[string]any) map[string]any {
	return map[string]any{
		"runtime":       raw["Runtime"],
		"memory_size":   raw["MemorySize"],
		"timeout":       raw["Timeout"],
		"architectures": raw["Architectures"],
	}
}

func dynamoProperties(raw map[string]any) map[string]any {
	_, streams := raw["StreamSpecification"]
	return map[string]any{"billing_mode": raw["BillingMode"], "stream_enabled": streams}
}

func logGroupProperties(raw map[string]any) map[string]any {
	return map[string]any{"retention_in_days": raw["RetentionInDays"]}
}

func queueProperties(raw map[string]any) map[string]any {
	return map[string]any{"fifo_queue": raw["FifoQueue"] == true}
}
