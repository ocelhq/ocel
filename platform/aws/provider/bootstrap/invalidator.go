package bootstrap

import (
	"context"
	"fmt"
)

const (
	tagInvalidatorAssetName = "tag-invalidator.zip"

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

func pinnedTagInvalidator() artifactPin {
	return artifactPin{version: TagInvalidatorArtifactVersion, sha256: TagInvalidatorArtifactSHA256}
}

func tagInvalidatorReleaseURL(version string) string {
	return fmt.Sprintf("https://github.com/ocelhq/ocel/releases/download/tag-invalidator-v%s/%s", version, tagInvalidatorAssetName)
}

func tagInvalidatorArtifactKey(p artifactPin) string {
	return fmt.Sprintf("%s/%s-%s.zip", tagInvalidatorKeyPrefix, p.version, p.digest())
}

func ensureTagInvalidatorArtifact(ctx context.Context, art Artifacts, bucket string, p artifactPin) (artifactCode, error) {
	return ensureArtifact(ctx, art, bucket, tagInvalidatorArtifactKey(p), tagInvalidatorReleaseURL(p.version), tagInvalidatorLabel, p)
}

func tagInvalidatorResources(code artifactCode, class string) string {
	if !code.present() {
		return ""
	}
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
      Description: "Execution role for this substrate's tag invalidator. Grants it the state table's stream, the ledger items naming which distributions to reach, and invalidation on this account's distributions - and nothing else. Managed by ocel bootstrap; deleting it leaves every front serving pages the origin already considers stale."
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
              - Effect: Allow
                Action: sqs:SendMessage
                Resource: !GetAtt TagInvalidatorDeadLetterQueue.Arn
  TagInvalidator:
    Type: AWS::Lambda::Function
    Properties:
      Description: "Ocel tag invalidator - turns each build's tag raises, read from the state table stream, into cache-tag invalidations on the distributions the ledger names for that project. Managed by ocel bootstrap; delete it and on-demand revalidation stops reaching the fronts."
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
          %s: !Ref StateTable
          %s: '%s'
  TagInvalidatorStream:
    Type: AWS::Lambda::EventSourceMapping
    Metadata:
      Description: "Binds the invalidator to the state table's stream, filtered to tag metadata items so unrelated writes cost nothing. This is the only trigger it has: delete it and invalidation goes quiet rather than failing - the table keeps accepting raises and no front hears about them."
    Properties:
      EventSourceArn: !GetAtt StateTable.StreamArn
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
		tagInvalidatorRuntime, tagInvalidatorArchitecture, tagInvalidatorHandler, tagInvalidatorMemoryMB, tagInvalidatorTimeoutSeconds,
		code.bucket, code.key,
		tagInvalidatorStateTableEnvVar, tagInvalidatorClassEnvVar, class,
		tagInvalidatorBatchSize, tagInvalidatorRetries, tagRecordStreamFilter)
}
