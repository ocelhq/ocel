package bootstrap

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	outputRevalidateQueueURL = "RevalidateQueueUrl"
	outputRevalidateQueueARN = "RevalidateQueueArn"

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

func revalidatorPlacement(bucket string) payloads.Placement {
	return payloads.At(bucket, revalidatorKeyPrefix, payloads.Revalidator())
}

func ensureRevalidatorPayload(ctx context.Context, store ObjectStore, bucket string) (payloads.Placement, error) {
	return payloads.Place(ctx, store, bucket, revalidatorKeyPrefix, revalidatorLabel, payloads.Revalidator())
}

func revalidateQueueResources(class string) string {
	queue, dlq := revalidateQueueNames(class)
	return fmt.Sprintf(`  RevalidateDeadLetterQueue:
    Type: AWS::SQS::Queue
    Metadata:
      Description: "Where an ISR refresh lands once the revalidator has failed it enough times. Anything in here is a page that was asked to re-render and never did, so a filling queue points at a broken origin rather than a broken queue."
    Properties:
      QueueName: %s
      FifoQueue: true
      MessageRetentionPeriod: %d
      KmsMasterKeyId: alias/aws/sqs
  RevalidateQueue:
    Type: AWS::SQS::Queue
    Metadata:
      Description: "The queue the edge sends admitted ISR refreshes to and the revalidator drains, FIFO and explicitly deduplicated so a stampede on one page renders once. Shared by every app in this substrate; delete it and background revalidation stops for all of them."
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

func revalidatorResources(code payloads.Placement) string {
	return fmt.Sprintf(`  RevalidatorRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Execution role for this substrate's ISR revalidator. Grants it the revalidation queue, the origin descriptors in the asset bucket and invoke on app functions only, so it can re-render an app but cannot touch state, variables or any other function Ocel runs in this account. Managed by ocel bootstrap; deleting it leaves the queue undrained."
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
                Resource: !Sub '${AssetBucketArn}/*/origin.json'
              - Effect: Allow
                Action:
                  - lambda:InvokeFunctionUrl
                  - lambda:InvokeFunction
                Resource: !Sub 'arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:*'
                Condition:
                  StringEquals:
                    'aws:ResourceTag/ocel:component': 'function'
  Revalidator:
    Type: AWS::Lambda::Function
    Properties:
      Description: "Ocel ISR revalidator - drains this substrate's revalidation queue, turning one deduplicated message into one signed render at the app's own origin. Managed by ocel bootstrap; delete it and stale pages stay stale."
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
          %s: !Ref AssetBucketName
  RevalidatorQueueConsumer:
    Type: AWS::Lambda::EventSourceMapping
    Metadata:
      Description: "Binds the revalidator to the revalidation queue under a concurrency cap, so a burst of expiring pages cannot crowd out live traffic at the apps' own origins. Delete it and messages accumulate until they age out - the queue stays healthy while nothing re-renders."
    Properties:
      EventSourceArn: !GetAtt RevalidateQueue.Arn
      FunctionName: !GetAtt Revalidator.Arn
      BatchSize: %d
      FunctionResponseTypes:
        - ReportBatchItemFailures
      ScalingConfig:
        MaximumConcurrency: %d
`, revalidatorRuntime, revalidatorArchitecture, revalidatorHandler, revalidatorMemoryMB, revalidatorTimeoutSeconds,
		code.Bucket, code.Key,
		revalidatorAssetBucketEnvVar,
		revalidatorBatchSize, revalidatorMaxConcurrency)
}

func revalidateQueueOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "URL of this substrate's ISR revalidation queue. The edge sends admitted refreshes to it and the revalidator drains it."
    Value: !Ref RevalidateQueue
  %s:
    Description: "ARN of this substrate's ISR revalidation queue, handed to whichever feature stack has to grant send on it."
    Value: !GetAtt RevalidateQueue.Arn
`, outputRevalidateQueueURL, outputRevalidateQueueARN)
}
