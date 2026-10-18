package bootstrap

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
)

const (
	VariablesTableIndexName = tagclock.IndexName

	outputVariablesTable      = "VariablesTableName"
	outputVariablesTableARN   = "VariablesTableArn"
	outputVariablesKeyARN     = "VariablesKeyArn"
	outputVariablesKeyBrought = "VariablesKeyBrought"

	VariablesKeyComponentTagKey   = "ocel:component"
	VariablesKeyComponentTagValue = "variables-key"
)

func variablesKeyResources(ns Namespace, tier environment.Tier) []cfn.Resource {
	return []cfn.Resource{
		{
			ID:   "VariablesKey",
			Type: "AWS::KMS::Key",
			Properties: map[string]any{
				"Description":       "Ocel: the key every encrypted variable of the " + string(tier) + " tier is encrypted under, written when a value is set and read by deploys and running functions. Scheduling its deletion strands those values - each has to be set again under a new key.",
				"EnableKeyRotation": true,
				"Tags": []any{
					map[string]any{"Key": VariablesKeyComponentTagKey, "Value": VariablesKeyComponentTagValue},
					map[string]any{"Key": naming.EnvTierTagKey, "Value": string(tier)},
				},
				"KeyPolicy": iam.Document([]iam.Statement{{
					Principal: iam.Principal{AWS: cfn.Sub("arn:aws:iam::${AWS::AccountId}:root")},
					Actions:   []string{"kms:*"},
					Resources: []string{iam.AnyResource},
				}}),
			},
		},
		{
			ID:   "VariablesKeyAlias",
			Type: "AWS::KMS::Alias",
			Metadata: cfn.Metadata{
				Description: "Stable name for the " + string(tier) + " tier's variable key, so an operator reading a key policy or a CloudTrail entry can tell which key it is without resolving the id.",
			},
			Properties: map[string]any{
				"AliasName":   ns.variablesKeyAliasFor(tier),
				"TargetKeyId": cfn.Ref("VariablesKey"),
			},
		},
	}
}

func variablesKeyOutputs() []cfn.Output {
	return []cfn.Output{{
		Name:        outputVariablesKeyARN,
		Description: "KMS key every encrypted value of this tier is encrypted under. A deploy decrypts through it, and so does each app's execution role.",
		Value:       cfn.GetAtt("VariablesKey", "Arn"),
	}}
}

func variablesTableResource(tier environment.Tier) cfn.Resource {
	return cfn.Resource{
		ID:   "VariablesTable",
		Type: "AWS::DynamoDB::Table",
		Metadata: cfn.Metadata{
			Description: "Every variable Ocel stores for the " + string(tier) + " tier, keyed by pk/sk, with the recent versions behind each value. Deleting it means every variable has to be set again.",
		},
		Properties: map[string]any{
			"BillingMode":          "PAY_PER_REQUEST",
			"AttributeDefinitions": keyedTableAttributes(),
			"KeySchema":            keySchema("pk", "sk"),
			"GlobalSecondaryIndexes": []any{
				map[string]any{
					"IndexName":  VariablesTableIndexName,
					"KeySchema":  keySchema("gsi1pk", "gsi1sk"),
					"Projection": map[string]any{"ProjectionType": "KEYS_ONLY"},
				},
			},
		},
	}
}

func variablesOutputs() []cfn.Output {
	return []cfn.Output{
		{
			Name:        outputVariablesTable,
			Description: "DynamoDB table storing every variable set for this tier, with its history. Kept apart from the state table so a variable read never touches deploy state.",
			Value:       cfn.Ref("VariablesTable"),
		},
		{
			Name:        outputVariablesTableARN,
			Description: "ARN of that table, read by the deploy that adds the env source sync so the sync's role reaches this table and no other.",
			Value:       cfn.GetAtt("VariablesTable", "Arn"),
		},
	}
}
