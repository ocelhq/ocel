package bootstrap

import (
	"context"
	"fmt"
)

// The account-global ISR revalidation queue and its consumer: the one place a
// stale route's re-render is decided for the whole fleet.
//
// The queue is the dedup. Every colo that finds the same entry stale sends the
// same (isrPrefix, route, lastModified) message under the same deduplication
// id, and SQS FIFO collapses them into one; the consumer turns that survivor
// into one signed trigger at the app's own origin, at a concurrency the event
// source mapping caps. That cap is the first bound this system has ever had on
// total origin pressure from a mass invalidation.
//
// The queue renders unconditionally; the consumer renders only when this
// provider build pins a revalidator artifact. The queue URL output is gated on
// the consumer for the reason recorded at revalidateQueueOutput: a queue with
// no drain must never be told to the edge.

const (
	// outputRevalidateQueueURL publishes the queue URL the deploy path binds
	// into every worker as OCEL_REVALIDATE_QUEUE_URL.
	outputRevalidateQueueURL = "RevalidateQueueUrl"

	// revalidatorAssetName is the file name the release asset is published
	// under. It matches what packages/revalidator/scripts/build-zip.mjs writes.
	revalidatorAssetName = "revalidator.zip"

	// revalidatorKeyPrefix is where the artifact lands in the account's artifact
	// bucket, on the same two-segment key shape the optimizer and the publisher
	// use and for the same reason: function artifacts are keyed
	// `<slug>/<function>/<hash>.zip`, so no deploy can land on it.
	revalidatorKeyPrefix = "ocel-revalidator"

	// revalidatorLabel names the artifact in every message the placement path
	// can fail with.
	revalidatorLabel = "revalidator"

	// What the artifact is built for and needs. It reads one small S3 object per
	// distinct deploy in a batch and sends one signed HEAD per record, so it is
	// bounded by network round trips rather than by CPU or heap.
	revalidatorRuntime      = "nodejs22.x"
	revalidatorArchitecture = "arm64"
	revalidatorHandler      = "index.handler"
	revalidatorMemoryMB     = 512

	// revalidatorTimeoutSeconds bounds one batch, and packages/revalidator sizes
	// it: ten records processed in order, each paying the handler's 10s trigger
	// budget and, in the worst case of ten distinct deploys, a 2s record read —
	// 120s — plus cold start and signing. The arithmetic lives in
	// packages/revalidator/src/limits.mts and is asserted in its
	// test/limits.test.mts; this constant is the template's half of it and must
	// stay below revalidateVisibilityTimeoutSeconds.
	revalidatorTimeoutSeconds = 150

	// revalidatorBatchSize is the FIFO maximum. Records in one batch are
	// processed in order and reported individually (ReportBatchItemFailures), so
	// a bigger batch is purely fewer invocations for the same work.
	revalidatorBatchSize = 10

	// revalidatorMaxConcurrency is the global render-drain bound: the most
	// concurrent invocations Lambda will run against this queue, and therefore
	// the most concurrent renders a mass tag invalidation can put on the origin.
	// Deliberately small — the whole point of the queue is that the origin no
	// longer sees one render per stale colo.
	revalidatorMaxConcurrency = 10

	// revalidateVisibilityTimeoutSeconds must outlast a whole batch, or the
	// consumer redelivers records it already processed successfully and DLQs
	// them (human decision G, amending the design doc's 60).
	revalidateVisibilityTimeoutSeconds = 300

	// revalidateRetentionSeconds bounds how long an undrained revalidation
	// lingers (human decision C, amending the design doc's 3600). A revalidation
	// older than the dedup window is worthless: each stale echo carries its own
	// lastModified and therefore its own dedup id, so at an hour a wedged
	// consumer accumulates a dozen per route per hour and renders every one of
	// them, superseded or not, on recovery. Five minutes bounds the backlog to
	// about one message per route.
	revalidateRetentionSeconds = 300

	// revalidateMaxReceiveCount is how many times a record may be received
	// before redrive retires it to the dead-letter queue.
	revalidateMaxReceiveCount = 5

	// revalidateDLQRetentionSeconds keeps a retired revalidation for the full 14
	// days SQS allows. What lands there is a diagnosis, not a work item — the
	// route it names has long since hard-expired — so the only cost of keeping
	// it is that the alarm stays lit until someone acknowledges it.
	revalidateDLQRetentionSeconds = 1209600

	// The alarms all evaluate one five-minute period. A message on the DLQ or an
	// erroring handler has no healthy rate to tolerate, and the backlog age
	// threshold is already a duration.
	revalidatorAlarmPeriodSeconds = 300
	revalidatorAlarmPeriods       = 1

	// revalidatorOldestMessageThresholdSeconds is how stale the head of the
	// queue may get before the consumer counts as wedged or the origin as down.
	// It matches the retention period: a message older than that is about to be
	// dropped, so alarming later than this would alarm on nothing.
	revalidatorOldestMessageThresholdSeconds = 300

	// revalidatorAssetBucketEnvVar names the bucket holding each deploy's
	// <isrPrefix>/origin.json — the record the consumer resolves an origin from,
	// written by cloud/aws/deploy. Unset, the consumer resolves nothing and
	// triggers nothing.
	revalidatorAssetBucketEnvVar = "OCEL_ASSET_BUCKET"
)

// revalidateQueueNames is the FIFO queue pair one substrate class provisions.
// The two classes are separate stacks in the same account, so the names have to
// differ or the second stack collides with the first.
func revalidateQueueNames(class string) (queue, dlq string) {
	if class == ClassPreview {
		return "ocel-revalidate-preview.fifo", "ocel-revalidate-preview-dlq.fifo"
	}
	return "ocel-revalidate.fifo", "ocel-revalidate-dlq.fifo"
}

func pinnedRevalidator() artifactPin {
	return artifactPin{version: RevalidatorArtifactVersion, sha256: RevalidatorArtifactSHA256}
}

// revalidatorReleaseURL is where the pinned asset is published. The tag is the
// contract the release step must satisfy; nothing discovers it.
func revalidatorReleaseURL(version string) string {
	return fmt.Sprintf("https://github.com/ocelhq/ocel/releases/download/revalidator-v%s/%s", version, revalidatorAssetName)
}

// revalidatorArtifactKey is content-addressed on the pinned digest, so a digest
// that moves lands at a new key and CloudFormation sees a code change.
func revalidatorArtifactKey(p artifactPin) string {
	return fmt.Sprintf("%s/%s-%s.zip", revalidatorKeyPrefix, p.version, p.digest())
}

// ensureRevalidatorArtifact places the pinned revalidator zip; see
// ensureArtifact for the fail-closed discipline it runs under.
func ensureRevalidatorArtifact(ctx context.Context, art Artifacts, bucket string, p artifactPin) (artifactCode, error) {
	return ensureArtifact(ctx, art, bucket, revalidatorArtifactKey(p), revalidatorReleaseURL(p.version), revalidatorLabel, p)
}

// revalidateQueueResources renders the revalidation queue and its dead-letter
// queue, in every substrate and whether or not this build pins a consumer: the
// queue is what the edge sends to, and an edge that finds it missing is an edge
// that cannot be granted anything against it.
//
// FIFO, because the deduplication is the design. Content-based deduplication is
// off — the sender always supplies an explicit id derived from the render it is
// asking for, which is not a hash of the body — and both the dedup scope and
// the throughput mode are left at their defaults: high-throughput mode forces
// message-group-scoped deduplication, which would dedupe per colo instead of
// globally and give back exactly the herd this queue exists to collapse. The
// volumes do not need it either, at hundreds of messages per stale event
// against 300 TPS per partition.
//
// Both queues are SSE-KMS rather than SQS-managed: every message carries the
// app's bypass token in x-prerender-revalidate, so both are secret-bearing
// stores and both belong under a key whose use is auditable in CloudTrail. The
// AWS-managed alias is the whole of it — a customer-managed key would add a key
// policy and a rotation lifecycle to a store whose contents are worthless after
// five minutes.
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

// revalidatorResources renders the consumer's execution role, function, event
// source mapping and alarms — or nothing when no artifact is available, which
// leaves the queue rendered and undrained and the edge unaware of it (see
// revalidateQueueOutput).
//
// The role is exactly what turning one message into one trigger takes. Its
// s3:GetObject is scoped to `*/origin.json` and must never be relaxed to `/*`:
// the edge holds s3:PutObject on `${AssetBucket.Arn}/*/fetch-cache/*` and
// writes fully-controlled JSON bodies there, and the message names the key this
// role reads. Round-two review of the consumer package demonstrated a working
// exfiltration from exactly that — a '#' in the message's isrPrefix truncated
// the appended /origin.json, aws4fetch signed the truncated path, and the
// consumer delivered the app's bypass token to an origin the edge had planted.
// The parser now rejects such a prefix, but IAM's '*' spans '/', so requiring
// the key to END in /origin.json is what makes this read grant and the edge's
// write grant disjoint — every key the edge can write ends in '.cache.json' —
// and that holds even if the parser regresses.
//
// The invoke grant is account-wide over Ocel-tagged functions, and that is
// accepted rather than overlooked. There is one consumer per substrate and it
// triggers renders for every app in it, so there is no app to scope to; the
// functions are Pulumi-autonamed, so there is no name prefix either. It is
// narrowed on the one axis that is available and correct — this account and
// this region, where the deploy path creates the Function URLs it triggers,
// against the edge user's own `arn:aws:lambda:*` — and no further. The design
// doc calls this condition "scoped by the ocel:app resource tag", which reads
// narrower than it is; it means "carries one at all".
//
// Three alarms, covering the ways revalidation stops. There is deliberately no
// absence alarm on the poller: unlike the publisher's stream, an empty
// revalidation queue is the healthy steady state.
//
// EXPECT THE DEAD-LETTER ALARM ON THE FIRST ROLLOUT. Every build already live
// when this lands has no origin.json, so each of its enqueued routes fails to
// resolve an origin through five receives and reaches the dead-letter queue.
// It clears once the retention period drains and every live build has been
// redeployed. Written down so the alarm's first real signal is not dismissed as
// rollout noise.
//
// The mapping has no OnFailure destination: DestinationConfig is a stream-source
// property, and an SQS source's failure path is the queue's own redrive policy,
// which revalidateQueueResources renders.
//
// Like the optimizer and the publisher, the function is deliberately unnamed and
// carries no ocel:app tag — it belongs to no app, and a fabricated tag would
// both be a lie and misclassify it for everything keyed off that tag, this
// role's own invoke condition included.
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
  RevalidatorDeadLetterAlarm:
    Type: AWS::CloudWatch::Alarm
    Properties:
      AlarmDescription: A revalidation exhausted its receives; that route will not refresh until it hard-expires. Expect this on the first rollout, for every build deployed before the origin record existed.
      Namespace: AWS/SQS
      MetricName: ApproximateNumberOfMessagesVisible
      Dimensions:
        - Name: QueueName
          Value: !GetAtt RevalidateDeadLetterQueue.QueueName
      Statistic: Maximum
      Period: %d
      EvaluationPeriods: %d
      Threshold: 0
      ComparisonOperator: GreaterThanThreshold
      TreatMissingData: notBreaching
  RevalidatorBacklogAlarm:
    Type: AWS::CloudWatch::Alarm
    Properties:
      AlarmDescription: The head of the revalidation queue is older than its own retention; the consumer is wedged or the origin is down, and stale routes are serving until they hard-expire.
      Namespace: AWS/SQS
      MetricName: ApproximateAgeOfOldestMessage
      Dimensions:
        - Name: QueueName
          Value: !GetAtt RevalidateQueue.QueueName
      Statistic: Maximum
      Period: %d
      EvaluationPeriods: %d
      Threshold: %d
      ComparisonOperator: GreaterThanThreshold
      TreatMissingData: notBreaching
  RevalidatorErrorAlarm:
    Type: AWS::CloudWatch::Alarm
    Properties:
      AlarmDescription: The ISR revalidator is throwing; the handler reports per-record failures rather than throwing, so an error here is a defect rather than an unrenderable route.
      Namespace: AWS/Lambda
      MetricName: Errors
      Dimensions:
        - Name: FunctionName
          Value: !Ref Revalidator
      Statistic: Sum
      Period: %d
      EvaluationPeriods: %d
      Threshold: 0
      ComparisonOperator: GreaterThanThreshold
      TreatMissingData: notBreaching
`, revalidatorRuntime, revalidatorArchitecture, revalidatorHandler, revalidatorMemoryMB, revalidatorTimeoutSeconds,
		code.bucket, code.key,
		revalidatorAssetBucketEnvVar,
		revalidatorBatchSize, revalidatorMaxConcurrency,
		revalidatorAlarmPeriodSeconds, revalidatorAlarmPeriods,
		revalidatorAlarmPeriodSeconds, revalidatorAlarmPeriods, revalidatorOldestMessageThresholdSeconds,
		revalidatorAlarmPeriodSeconds, revalidatorAlarmPeriods)
}

// revalidateQueueOutput publishes the queue URL — but only when a consumer was
// rendered, which is the whole of human decision F.
//
// The queue exists in every substrate; the consumer exists only in a build that
// pins the artifact. Publishing the URL on the strength of the queue alone
// would let a deploy in between bind it, and then the failure is silent in the
// exact shape this epic is named after: the edge enqueues, the send succeeds,
// the thunk reports "landed", the colo sentinel re-arms, and the route stops
// revalidating until it hard-expires, with nothing anywhere reporting an error.
// An absent output leaves the var unbound, which is the edge's own signal to
// keep rendering through the origin exactly as it does today.
func revalidateQueueOutput(code artifactCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  %s:
    Description: URL of the ISR revalidation queue, sent to by the edge and drained by the revalidator.
    Value: !Ref RevalidateQueue
`, outputRevalidateQueueURL)
}
