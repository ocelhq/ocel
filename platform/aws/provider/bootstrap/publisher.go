package bootstrap

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	tagPublisherKeyPrefix = "ocel-tag-publisher"

	tagPublisherLabel = "tag publisher"

	tagPublisherRuntime      = "nodejs22.x"
	tagPublisherArchitecture = "arm64"
	tagPublisherHandler      = "index.handler"
	tagPublisherMemoryMB     = 512

	tagPublisherTimeoutSeconds = 60

	tagPublisherBatchSize = 100
	tagPublisherRetries   = 5

	tagPublisherDLQRetentionSeconds = 1209600

	tagPublisherAssetBucketEnvVar = "OCEL_ASSET_BUCKET"
	tagPublisherWriterParamEnvVar = "OCEL_ISR_WRITER_PARAM"
	tagPublisherSeedParamEnvVar   = "OCEL_ISR_WRITER_SEED_PARAM"
)

const tagRecordStreamFilter = `{"dynamodb":{"Keys":{"pk":{"S":[{"prefix":"PROJECT#"}]},"sk":{"S":["#META"]}}}}`

func tagPublisherPlacement(bucket string) payloads.Placement {
	return payloads.At(bucket, tagPublisherKeyPrefix, payloads.TagPublisher())
}

func ensureTagPublisherPayload(ctx context.Context, store ObjectStore, bucket string) (payloads.Placement, error) {
	return payloads.Place(ctx, store, bucket, tagPublisherKeyPrefix, tagPublisherLabel, payloads.TagPublisher())
}

func tagPublisherResources(code payloads.Placement, class string) string {
	writerParam, seedParam := isrWriterParamNames(class)
	return fmt.Sprintf(`  TagPublisherDeadLetterQueue:
    Type: AWS::SQS::Queue
    Metadata:
      Description: "Tag snapshots the publisher gave up on. Anything in here is an invalidation that never reached the edge, so those builds serve pages the origin calls stale."
    Properties:
      MessageRetentionPeriod: %d
      SqsManagedSseEnabled: true
  TagPublisherRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Execution role for this bootstrap's tag-snapshot publisher: the state table's stream, the asset bucket and the two ISR writer parameters, and nothing else."
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
        - PolicyName: ocel-tag-publisher
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
                Action:
                  - s3:GetObject
                  - s3:PutObject
                Resource: !Sub '${AssetBucketArn}/*'
              - Effect: Allow
                Action: sqs:SendMessage
                Resource: !GetAtt TagPublisherDeadLetterQueue.Arn
              - Effect: Allow
                Action: ssm:GetParameter
                Resource:
                  - !Sub 'arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter%s'
                  - !Sub 'arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter%s'
              - Effect: Allow
                Action: kms:Decrypt
                Resource: '*'
                Condition:
                  StringEquals:
                    kms:EncryptionContext:PARAMETER_ARN:
                      - !Sub 'arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter%s'
                      - !Sub 'arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter%s'
  TagPublisher:
    Type: AWS::Lambda::Function
    Properties:
      Description: "Ocel tag-snapshot publisher - the single writer of every build's tag clock at the edge, fed by the state table stream."
      Runtime: %s
      Architectures:
        - %s
      Handler: %s
      MemorySize: %d
      Timeout: %d
      Role: !GetAtt TagPublisherRole.Arn
      Code:
        S3Bucket: %s
        S3Key: %s
      Environment:
        Variables:
          %s: !Ref AssetBucketName
          %s: '%s'
          %s: '%s'
  TagPublisherStream:
    Type: AWS::Lambda::EventSourceMapping
    Metadata:
      Description: "Binds the publisher to the state table's stream, filtered to tag metadata items so unrelated writes cost nothing. Its only trigger."
    Properties:
      EventSourceArn: !Ref StateTableStreamArn
      FunctionName: !GetAtt TagPublisher.Arn
      StartingPosition: LATEST
      BatchSize: %d
      MaximumBatchingWindowInSeconds: 0
      FunctionResponseTypes:
        - ReportBatchItemFailures
      MaximumRetryAttempts: %d
      DestinationConfig:
        OnFailure:
          Destination: !GetAtt TagPublisherDeadLetterQueue.Arn
      FilterCriteria:
        Filters:
          - Pattern: '%s'
`, tagPublisherDLQRetentionSeconds,
		writerParam, seedParam, writerParam, seedParam,
		tagPublisherRuntime, tagPublisherArchitecture, tagPublisherHandler, tagPublisherMemoryMB, tagPublisherTimeoutSeconds,
		code.Bucket, code.Key,
		tagPublisherAssetBucketEnvVar, tagPublisherWriterParamEnvVar, writerParam, tagPublisherSeedParamEnvVar, seedParam,
		tagPublisherBatchSize, tagPublisherRetries, tagRecordStreamFilter)
}

func isrWriterParamNames(class string) (writer, seed string) {
	names := edgeNamesByClass[class]
	return names.isrWriterParam, names.isrWriterSeedParam
}
