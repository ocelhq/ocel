package bootstrap

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	tagInvalidatorKeyPrefix = "ocel-tag-invalidator"

	tagInvalidatorLabel = "tag invalidator"

	tagInvalidatorRuntime      = "nodejs22.x"
	tagInvalidatorArchitecture = "arm64"
	tagInvalidatorHandler      = "index.handler"
	tagInvalidatorMemoryMB     = 512

	tagInvalidatorTimeoutSeconds = 60

	tagInvalidatorBatchSize = 100
	tagInvalidatorRetries   = 5

	tagInvalidatorDLQRetentionSeconds = 1209600

	tagInvalidatorStateTableEnvVar = "OCEL_STATE_TABLE"
	tagInvalidatorClassEnvVar      = "OCEL_INFRA_CLASS"
)

func tagInvalidatorPlacement(bucket string) payloads.Placement {
	return payloads.At(bucket, tagInvalidatorKeyPrefix, payloads.TagInvalidator())
}

func ensureTagInvalidatorPayload(ctx context.Context, store ObjectStore, bucket string) (payloads.Placement, error) {
	return payloads.Place(ctx, store, bucket, tagInvalidatorKeyPrefix, tagInvalidatorLabel, payloads.TagInvalidator())
}

func tagInvalidatorResources(code payloads.Placement, class string) string {
	command := classCommand(class)
	return fmt.Sprintf(`  TagInvalidatorDeadLetterQueue:
    Type: AWS::SQS::Queue
    Metadata:
      Description: "Where a batch of tag raises lands after the invalidator has exhausted its retries. Anything in here is a page the origin has already re-rendered that the fronts are still serving the old copy of, until its cache-control window ends. Draining it by hand is a diagnosis, not a repair."
    Properties:
      MessageRetentionPeriod: %d
      SqsManagedSseEnabled: true
  TagInvalidatorRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Execution role for this bootstrap's tag invalidator. Grants it the state table's stream, the ledger items naming which distributions to reach, and invalidation on this account's distributions - and nothing else. Managed by %s; deleting it leaves every front serving pages the origin already considers stale."
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
        - PolicyName: ocel-tag-invalidator
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action:
                  - dynamodb:DescribeStream
                  - dynamodb:GetRecords
                  - dynamodb:GetShardIterator
                Resource: !Ref StateTableStreamArn
              - Effect: Allow
                Action: dynamodb:ListStreams
                Resource: '*'
              - Effect: Allow
                Action: dynamodb:GetItem
                Resource: !Ref StateTableArn
              - Effect: Allow
                Action: cloudfront:CreateInvalidation
                Resource: !Sub 'arn:aws:cloudfront::${AWS::AccountId}:distribution/*'
              - Effect: Allow
                Action: sqs:SendMessage
                Resource: !GetAtt TagInvalidatorDeadLetterQueue.Arn
  TagInvalidator:
    Type: AWS::Lambda::Function
    Properties:
      Description: "Ocel tag invalidator - turns each build's tag raises, read from the state table stream, into cache-tag invalidations on the distributions the ledger names for that project. Managed by %s; delete it and on-demand revalidation stops reaching the fronts."
      Runtime: %s
      Architectures:
        - %s
      Handler: %s
      MemorySize: %d
      Timeout: %d
      Role: !GetAtt TagInvalidatorRole.Arn
      Code:
        S3Bucket: %s
        S3Key: %s
      Environment:
        Variables:
          %s: !Ref StateTableName
          %s: '%s'
  TagInvalidatorStream:
    Type: AWS::Lambda::EventSourceMapping
    Metadata:
      Description: "Binds the invalidator to the state table's stream, filtered to tag metadata items so unrelated writes cost nothing. This is the only trigger it has: delete it and invalidation goes quiet rather than failing - the table keeps accepting raises and no front hears about them."
    Properties:
      EventSourceArn: !Ref StateTableStreamArn
      FunctionName: !GetAtt TagInvalidator.Arn
      StartingPosition: LATEST
      BatchSize: %d
      MaximumBatchingWindowInSeconds: 0
      FunctionResponseTypes:
        - ReportBatchItemFailures
      MaximumRetryAttempts: %d
      DestinationConfig:
        OnFailure:
          Destination: !GetAtt TagInvalidatorDeadLetterQueue.Arn
      FilterCriteria:
        Filters:
          - Pattern: '%s'
`, tagInvalidatorDLQRetentionSeconds,
		command, command, tagInvalidatorRuntime, tagInvalidatorArchitecture, tagInvalidatorHandler, tagInvalidatorMemoryMB, tagInvalidatorTimeoutSeconds,
		code.Bucket, code.Key,
		tagInvalidatorStateTableEnvVar, tagInvalidatorClassEnvVar, class,
		tagInvalidatorBatchSize, tagInvalidatorRetries, tagRecordStreamFilter)
}
