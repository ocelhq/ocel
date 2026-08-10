package bootstrap

import (
	"context"
	"fmt"
)

const (
	outputRevalidateQueueURL = "RevalidateQueueUrl"

	revalidatorAssetName = "revalidator.zip"

	revalidatorKeyPrefix = "ocel-revalidator"

	revalidatorLabel = "revalidator"

	revalidatorRuntime      = "nodejs22.x"
	revalidatorArchitecture = "arm64"
	revalidatorHandler      = "index.handler"
	revalidatorMemoryMB     = 512

	revalidatorTimeoutSeconds = 150

	revalidatorBatchSize = 10

	revalidatorMaxConcurrency = 10

	revalidateVisibilityTimeoutSeconds = 300

	revalidateRetentionSeconds = 300

	revalidateMaxReceiveCount = 5

	revalidateDLQRetentionSeconds = 1209600

	revalidatorAssetBucketEnvVar = "OCEL_ASSET_BUCKET"
)

func revalidateQueueNames(class string) (queue, dlq string) {
	if class == ClassPreview {
		return "ocel-revalidate-preview.fifo", "ocel-revalidate-preview-dlq.fifo"
	}
	return "ocel-revalidate.fifo", "ocel-revalidate-dlq.fifo"
}

func pinnedRevalidator() artifactPin {
	return artifactPin{version: RevalidatorArtifactVersion, sha256: RevalidatorArtifactSHA256}
}

func revalidatorReleaseURL(version string) string {
	return fmt.Sprintf("https://github.com/ocelhq/ocel/releases/download/revalidator-v%s/%s", version, revalidatorAssetName)
}

func revalidatorArtifactKey(p artifactPin) string {
	return fmt.Sprintf("%s/%s-%s.zip", revalidatorKeyPrefix, p.version, p.digest())
}

func ensureRevalidatorArtifact(ctx context.Context, art Artifacts, bucket string, p artifactPin) (artifactCode, error) {
	return ensureArtifact(ctx, art, bucket, revalidatorArtifactKey(p), revalidatorReleaseURL(p.version), revalidatorLabel, p)
}

func revalidateQueueResources(class string) string {
	queue, dlq := revalidateQueueNames(class)
	return fmt.Sprintf(`  RevalidateDeadLetterQueue:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: %s
      FifoQueue: true
      MessageRetentionPeriod: %d
      KmsMasterKeyId: alias/aws/sqs
  RevalidateQueue:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: %s
      FifoQueue: true
      ContentBasedDeduplication: false
      VisibilityTimeout: %d
      MessageRetentionPeriod: %d
      KmsMasterKeyId: alias/aws/sqs
      RedrivePolicy:
        deadLetterTargetArn: !GetAtt RevalidateDeadLetterQueue.Arn
        maxReceiveCount: %d
`, dlq, revalidateDLQRetentionSeconds, queue,
		revalidateVisibilityTimeoutSeconds, revalidateRetentionSeconds, revalidateMaxReceiveCount)
}

func revalidatorResources(code artifactCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  RevalidatorRole:
    Type: AWS::IAM::Role
    Properties:
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
        - PolicyName: ocel-revalidator
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action:
                  - sqs:ReceiveMessage
                  - sqs:DeleteMessage
                  - sqs:GetQueueAttributes
                Resource: !GetAtt RevalidateQueue.Arn
              - Effect: Allow
                Action: kms:Decrypt
                Resource: '*'
                Condition:
                  StringEquals:
                    kms:ViaService: !Sub 'sqs.${AWS::Region}.amazonaws.com'
              - Effect: Allow
                Action: s3:GetObject
                Resource: !Sub '${AssetBucket.Arn}/*/origin.json'
              - Effect: Allow
                Action:
                  - lambda:InvokeFunctionUrl
                  - lambda:InvokeFunction
                Resource: !Sub 'arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:*'
                Condition:
                  'Null':
                    'aws:ResourceTag/ocel:app': 'false'
  Revalidator:
    Type: AWS::Lambda::Function
    Properties:
      Description: Ocel ISR revalidator - turns one deduplicated queue message into one signed render at the app's own origin.
      Runtime: %s
      Architectures:
        - %s
      Handler: %s
      MemorySize: %d
      Timeout: %d
      Role: !GetAtt RevalidatorRole.Arn
      Code:
        S3Bucket: %s
        S3Key: %s
      Environment:
        Variables:
          %s: !Ref AssetBucket
  RevalidatorQueueConsumer:
    Type: AWS::Lambda::EventSourceMapping
    Properties:
      EventSourceArn: !GetAtt RevalidateQueue.Arn
      FunctionName: !GetAtt Revalidator.Arn
      BatchSize: %d
      FunctionResponseTypes:
        - ReportBatchItemFailures
      ScalingConfig:
        MaximumConcurrency: %d
`, revalidatorRuntime, revalidatorArchitecture, revalidatorHandler, revalidatorMemoryMB, revalidatorTimeoutSeconds,
		code.bucket, code.key,
		revalidatorAssetBucketEnvVar,
		revalidatorBatchSize, revalidatorMaxConcurrency)
}

func revalidateQueueOutput(code artifactCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  %s:
    Description: URL of the ISR revalidation queue, sent to by the edge and drained by the revalidator.
    Value: !Ref RevalidateQueue
`, outputRevalidateQueueURL)
}
