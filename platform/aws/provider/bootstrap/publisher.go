package bootstrap

import (
	"context"
	"fmt"
)

const (
	tagPublisherAssetName = "tag-publisher.zip"

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

const tagPublisherFilter = `{"dynamodb":{"Keys":{"pk":{"S":[{"prefix":"PROJECT#"}]},"sk":{"S":["#META"]}}}}`

func pinnedTagPublisher() artifactPin {
	return artifactPin{version: TagPublisherArtifactVersion, sha256: TagPublisherArtifactSHA256}
}

func tagPublisherReleaseURL(version string) string {
	return fmt.Sprintf("https://github.com/ocelhq/ocel/releases/download/tag-publisher-v%s/%s", version, tagPublisherAssetName)
}

func tagPublisherArtifactKey(p artifactPin) string {
	return fmt.Sprintf("%s/%s-%s.zip", tagPublisherKeyPrefix, p.version, p.digest())
}

func ensureTagPublisherArtifact(ctx context.Context, art Artifacts, bucket string, p artifactPin) (artifactCode, error) {
	return ensureArtifact(ctx, art, bucket, tagPublisherArtifactKey(p), tagPublisherReleaseURL(p.version), tagPublisherLabel, p)
}

func tagPublisherResources(code artifactCode, class string) string {
	if !code.present() {
		return ""
	}
	writerParam, seedParam := isrWriterParamNames(class)
	return fmt.Sprintf(`  TagPublisherDeadLetterQueue:
    Type: AWS::SQS::Queue
    Metadata:
      Description: "Where a batch of tag snapshots lands after the publisher has exhausted its retries. Anything in here is a cache invalidation that never reached the edge, so a non-empty queue means some builds are serving pages the origin already considers stale. Draining it by hand is a diagnosis, not a repair."
    Properties:
      MessageRetentionPeriod: %d
      SqsManagedSseEnabled: true
  TagPublisherRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Execution role for this substrate's tag-snapshot publisher. Grants it the state table's stream, the asset bucket and the two ISR writer parameters, and nothing else. Managed by ocel bootstrap; deleting it stops every origin-raised invalidation before it reaches the edge."
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
                Resource: !GetAtt StateTable.StreamArn
              - Effect: Allow
                Action: dynamodb:ListStreams
                Resource: '*'
              - Effect: Allow
                Action:
                  - s3:GetObject
                  - s3:PutObject
                Resource: !Sub '${AssetBucket.Arn}/*'
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
      Description: "Ocel tag-snapshot publisher - the single writer of every build's tag clock in this substrate, fed by the state table stream. Managed by ocel bootstrap; delete it and origin-raised invalidations never reach the edge."
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
          %s: !Ref AssetBucket
          %s: '%s'
          %s: '%s'
  TagPublisherStream:
    Type: AWS::Lambda::EventSourceMapping
    Metadata:
      Description: "Binds the publisher to the state table's stream, filtered to tag metadata items so unrelated writes cost nothing. This is the only trigger the publisher has: delete it and invalidation goes quiet rather than failing - the table keeps accepting writes and nothing publishes them onward."
    Properties:
      EventSourceArn: !GetAtt StateTable.StreamArn
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
		code.bucket, code.key,
		tagPublisherAssetBucketEnvVar, tagPublisherWriterParamEnvVar, writerParam, tagPublisherSeedParamEnvVar, seedParam,
		tagPublisherBatchSize, tagPublisherRetries, tagPublisherFilter)
}

func isrWriterParamNames(class string) (writer, seed string) {
	names := edgeNamesByClass[class]
	return names.isrWriterParam, names.isrWriterSeedParam
}
