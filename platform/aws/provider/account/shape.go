package account

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

type ShapeOption func(*partInputs)

func WithVariablesKey(arn string) ShapeOption {
	return func(in *partInputs) { in.variablesKey = arn }
}

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

func Shape(ns Namespace, tier environment.Tier, parts []string, options ...ShapeOption) ([]pricing.Shaped, error) {
	shapes, err := ShapeParts(ns, tier, parts, options...)
	if err != nil {
		return nil, err
	}
	shaped := slices.Concat(shapes[provider.BasePart], shapes[RuntimeLayersPart])
	for _, name := range parts {
		shaped = append(shaped, shapes[name]...)
	}
	return shaped, nil
}

func shapeTemplate(template cfn.Template) ([]pricing.Shaped, error) {
	var out []pricing.Shaped
	for _, resource := range template.Resources {
		typ, priced := shapedTypes[resource.Type]
		if !priced {
			continue
		}
		properties := map[string]any{}
		if typ.properties != nil && len(resource.Properties) > 0 {
			raw, err := decodedProperties(resource)
			if err != nil {
				return nil, err
			}
			properties = typ.properties(raw)
		}
		out = append(out, pricing.Shaped{Name: resource.ID, Type: typ.token, Properties: properties})
	}
	return out, nil
}

func decodedProperties(resource cfn.Resource) (map[string]any, error) {
	encoded, err := json.Marshal(resource.Properties)
	if err != nil {
		return nil, fmt.Errorf("shape %s: %w", resource.ID, err)
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("shape %s: %w", resource.ID, err)
	}
	return decoded, nil
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
