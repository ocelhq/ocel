package bootstrap

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

const (
	outputTagInvalidatorRoleARN = "TagInvalidatorRoleArn"
	OutputEdgeInvokeRoleARN     = "EdgeInvokeRoleArn"
)

func (n Namespace) TagInvalidatorRoleARN(account string, tier environment.Tier) string {
	return "arn:aws:iam::" + account + ":role" + n.bootstrapRolePathFor(tier) + taginvalidator.NamesFor(string(n), tier).Role
}

func tagInvalidatorRoleResource(ns Namespace, tier environment.Tier) cfn.Resource {
	names := taginvalidator.NamesFor(string(ns), tier)
	seedParam, _ := PurgeSeedParamFor(ns, tier, KindCloudflare)
	seedARN := cfn.Sub("arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter" + seedParam)
	return cfn.Resource{
		ID:   "TagInvalidatorRole",
		Type: "AWS::IAM::Role",
		Metadata: cfn.Metadata{
			Description: "The role the " + string(tier) + " tag invalidator runs as. It reaches past the app boundary, to CloudFront invalidation and the Cloudflare purge seed; the first deploy that revalidates makes its function.",
		},
		Properties: map[string]any{
			"RoleName":                 names.Role,
			"Path":                     ns.bootstrapRolePathFor(tier),
			"Description":              "Execution role for this bootstrap's tag invalidator: the state table's stream, the items naming which fronts it reaches, invalidation on the CloudFront distributions Ocel tagged for this tier, and the seed that signs a purge sent to a Cloudflare front.",
			"AssumeRolePolicyDocument": iam.AssumeRoleDocument(iam.LambdaPrincipal),
			"ManagedPolicyArns":        []string{iam.LambdaBasicExecutionPolicyARN},
			"Policies": []any{map[string]any{
				"PolicyName": ns.PolicyName("tag-invalidator"),
				"PolicyDocument": iam.Document([]iam.Statement{
					{
						Actions:        []string{"dynamodb:DescribeStream", "dynamodb:GetRecords", "dynamodb:GetShardIterator"},
						ResourceValues: []any{cfn.GetAtt("StateTable", "StreamArn")},
					},
					{Actions: []string{"dynamodb:ListStreams"}, Resources: []string{iam.AnyResource}},
					{Actions: []string{"dynamodb:GetItem"}, ResourceValues: []any{cfn.GetAtt("StateTable", "Arn")}},
					{
						Actions:        []string{"cloudfront:CreateInvalidation"},
						ResourceValues: []any{cfn.Sub("arn:aws:cloudfront::${AWS::AccountId}:distribution/*")},
						Condition: map[string]any{"StringEquals": map[string]any{
							"aws:ResourceTag/" + managedByTagKey:      managedByTagValue,
							"aws:ResourceTag/" + naming.EnvTierTagKey: string(tier),
						}},
					},
					{
						Actions:        []string{"sqs:SendMessage"},
						ResourceValues: []any{cfn.Sub("arn:aws:sqs:${AWS::Region}:${AWS::AccountId}:" + names.Queue)},
					},
					{Actions: []string{"ssm:GetParameter"}, ResourceValues: []any{seedARN}},
					{
						Actions:   []string{"kms:Decrypt"},
						Resources: []string{iam.AnyResource},
						Condition: map[string]any{"StringEquals": map[string]any{
							"kms:EncryptionContext:PARAMETER_ARN": seedARN,
							"kms:ViaService":                      cfn.Sub("ssm.${AWS::Region}.amazonaws.com"),
						}},
					},
				}),
			}},
		},
	}
}

func edgeInvokeRoleResource(ns Namespace, tier environment.Tier) cfn.Resource {
	return cfn.Resource{
		ID:   "EdgeInvokeRole",
		Type: "AWS::IAM::Role",
		Metadata: cfn.Metadata{
			Description: "The role API Gateway assumes to invoke this account's root functions and to read a release's static assets out of the asset bucket. Every REST API Ocel raises in the " + string(tier) + " tier names it, so deleting it blanks every deployment API Gateway fronts.",
		},
		Properties: map[string]any{
			"RoleName":                 ns.EdgeInvokeRoleName(tier),
			"Description":              "API Gateway assumes this role to invoke Ocel's root functions and read a release's static assets.",
			"AssumeRolePolicyDocument": iam.AssumeRoleDocument(iam.APIGatewayPrincipal),
			"Policies": []any{map[string]any{
				"PolicyName": ns.PolicyName("edge-invoke"),
				"PolicyDocument": iam.Document([]iam.Statement{
					{
						Actions:        []string{"lambda:InvokeFunction"},
						ResourceValues: []any{cfn.Sub("arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:*")},
						Condition: map[string]any{"StringEquals": map[string]any{
							"aws:ResourceTag/ocel:component":          "function",
							"aws:ResourceTag/" + naming.EnvTierTagKey: string(tier),
						}},
					},
					{Actions: []string{"s3:GetObject"}, ResourceValues: []any{cfn.Sub("${AssetBucket.Arn}/*")}},
				}),
			}},
		},
	}
}

func baseRoleOutputs() []cfn.Output {
	return []cfn.Output{
		{
			Name:        outputTagInvalidatorRoleARN,
			Description: "ARN of the role the tag invalidator runs as; the first deploy that revalidates makes the function under it.",
			Value:       cfn.GetAtt("TagInvalidatorRole", "Arn"),
		},
		{
			Name:        OutputEdgeInvokeRoleARN,
			Description: "ARN of the role API Gateway assumes to invoke this account's root functions and read a release's static assets. Every deploy an API Gateway fronts names it on the integrations it raises.",
			Value:       cfn.GetAtt("EdgeInvokeRole", "Arn"),
		},
	}
}
