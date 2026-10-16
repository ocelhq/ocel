package bootstrap

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
)

const (
	outputTagInvalidatorRoleARN = "TagInvalidatorRoleArn"
	OutputEdgeInvokeRoleARN     = "EdgeInvokeRoleArn"
)

func (n Namespace) TagInvalidatorRoleARN(account string, tier environment.Tier) string {
	return "arn:aws:iam::" + account + ":role" + n.bootstrapRolePathFor(tier) + taginvalidator.NamesFor(string(n), tier).Role
}

func tagInvalidatorRoleResource(ns Namespace, tier environment.Tier) string {
	names := taginvalidator.NamesFor(string(ns), tier)
	seedParam, _ := PurgeSeedParamFor(ns, tier, KindCloudflare)
	return fmt.Sprintf(`  TagInvalidatorRole:
    Type: AWS::IAM::Role
    Metadata:
      Description: "The role the %[1]s tag invalidator runs as. It reaches past the app boundary, to CloudFront invalidation and the Cloudflare purge seed; the first deploy that revalidates makes its function."
    Properties:
      RoleName: %[2]s
      Path: %[3]s
      Description: "Execution role for this bootstrap's tag invalidator: the state table's stream, the items naming which fronts it reaches, invalidation on the CloudFront distributions Ocel tagged for this tier, and the seed that signs a purge sent to a Cloudflare front."
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: lambda.amazonaws.com
            Action: sts:AssumeRole
      ManagedPolicyArns:
        - arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
      Policies:
        - PolicyName: %[4]s
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action:
                  - dynamodb:DescribeStream
                  - dynamodb:GetRecords
                  - dynamodb:GetShardIterator
                Resource: !GetAtt StateTable.StreamArn
              - Effect: Allow
                Action: dynamodb:ListStreams
                Resource: '*'
              - Effect: Allow
                Action: dynamodb:GetItem
                Resource: !GetAtt StateTable.Arn
              - Effect: Allow
                Action: cloudfront:CreateInvalidation
                Resource: !Sub 'arn:aws:cloudfront::${AWS::AccountId}:distribution/*'
                Condition:
                  StringEquals:
                    'aws:ResourceTag/%[7]s': '%[8]s'
                    'aws:ResourceTag/%[9]s': '%[1]s'
              - Effect: Allow
                Action: sqs:SendMessage
                Resource: !Sub 'arn:aws:sqs:${AWS::Region}:${AWS::AccountId}:%[5]s'
              - Effect: Allow
                Action: ssm:GetParameter
                Resource: !Sub 'arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter%[6]s'
              - Effect: Allow
                Action: kms:Decrypt
                Resource: '*'
                Condition:
                  StringEquals:
                    kms:EncryptionContext:PARAMETER_ARN: !Sub 'arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter%[6]s'
                    kms:ViaService: !Sub 'ssm.${AWS::Region}.amazonaws.com'
`, tier, names.Role, ns.bootstrapRolePathFor(tier), ns.PolicyName("tag-invalidator"), names.Queue, seedParam, managedByTagKey, managedByTagValue, naming.EnvTierTagKey)
}

func edgeInvokeRoleResource(ns Namespace, tier environment.Tier) string {
	return fmt.Sprintf(`  EdgeInvokeRole:
    Type: AWS::IAM::Role
    Metadata:
      Description: "The role API Gateway assumes to invoke this account's root functions and to read a release's static assets out of the asset bucket. Every REST API Ocel raises in the %s tier names it, so deleting it blanks every deployment API Gateway fronts."
    Properties:
      RoleName: %s
      Description: "API Gateway assumes this role to invoke Ocel's root functions and read a release's static assets."
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: apigateway.amazonaws.com
            Action: sts:AssumeRole
      Policies:
        - PolicyName: %s
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: lambda:InvokeFunction
                Resource: !Sub 'arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:*'
                Condition:
                  StringEquals:
                    'aws:ResourceTag/ocel:component': 'function'
                    'aws:ResourceTag/%s': '%s'
              - Effect: Allow
                Action: s3:GetObject
                Resource: !Sub '${AssetBucket.Arn}/*'
`, tier, ns.EdgeInvokeRoleName(tier), ns.PolicyName("edge-invoke"), naming.EnvTierTagKey, tier)
}

func baseRoleOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "ARN of the role the tag invalidator runs as; the first deploy that revalidates makes the function under it."
    Value: !GetAtt TagInvalidatorRole.Arn
  %s:
    Description: "ARN of the role API Gateway assumes to invoke this account's root functions and read a release's static assets. Every deploy an API Gateway fronts names it on the integrations it raises."
    Value: !GetAtt EdgeInvokeRole.Arn
`, outputTagInvalidatorRoleARN, OutputEdgeInvokeRoleARN)
}
