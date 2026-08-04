package bootstrap

import (
	"context"
	"fmt"
)

// The account-global tag-snapshot publisher: the Lambda that consumes the state
// table's stream and is the single writer of every build's tag-clock replica.
//
// A tag invalidation is durable the moment it lands in the state table, and that
// write IS the raise (epic decision 1). Nothing publishes on the hot path any
// more: this consumer picks the record off the stream, writes the S3 copy
// directly, and posts the raise to that build's snapshot Durable Object, which
// owns the R2 write. One publisher per build, and no writer left contending on
// a key R2 rate-limits at one write a second.
//
// It is at-least-once and must stay idempotent: an event source mapping has up
// to two sanctioned readers per shard, so there is no "exactly one publisher" to
// be had. The merge is monotone, which is what makes that harmless.

const (
	// tagPublisherAssetName is the file name the release asset is published
	// under. It matches what packages/tag-publisher/scripts/build-zip.mjs writes.
	tagPublisherAssetName = "tag-publisher.zip"

	// tagPublisherKeyPrefix is where the artifact lands in the account's artifact
	// bucket, on the same two-segment key shape the optimizer uses and for the
	// same reason: function artifacts are keyed `<slug>/<function>/<hash>.zip`,
	// so no deploy can land on it.
	tagPublisherKeyPrefix = "ocel-tag-publisher"

	// tagPublisherLabel names the artifact in every message the placement path
	// can fail with.
	tagPublisherLabel = "tag publisher"

	// What the artifact is built for and needs. It reads one S3 object per build
	// in a batch, merges in memory and posts a small JSON body, so it is bounded
	// by network round trips rather than by CPU or heap.
	tagPublisherRuntime      = "nodejs22.x"
	tagPublisherArchitecture = "arm64"
	tagPublisherHandler      = "index.handler"
	tagPublisherMemoryMB     = 512

	// tagPublisherTimeoutSeconds bounds one batch. A batch fans out over the
	// builds it touches, so the wall clock is a few round trips rather than one
	// per record — but a timeout kills the whole batch, and a killed batch is
	// retried and eventually bisected, so overshooting is cheaper than clipping
	// a batch that was about to finish.
	tagPublisherTimeoutSeconds = 60

	// tagPublisherBatchSize and tagPublisherRetries. Retries are bounded so a
	// permanently failing batch reaches the DLQ instead of blocking its shard
	// forever: a stream shard is ordered, and an un-retirable record at its head
	// stalls every later invalidation for that shard.
	tagPublisherBatchSize = 100
	tagPublisherRetries   = 5

	// tagPublisherDLQRetentionSeconds keeps a failed batch's pointer around for
	// the full 14 days SQS allows. What lands there is a diagnosis, not a work
	// item — by the time anyone reads it the stream records themselves are gone
	// — so the only cost of keeping it is that the alarm stays lit until someone
	// acknowledges it, which is the point.
	tagPublisherDLQRetentionSeconds = 1209600

	// The DLQ alarm fires on the first message: a batch reaching the DLQ means
	// invalidations for those builds were dropped, and there is no healthy rate
	// of that. Missing data is not breaching — an empty queue publishes no
	// datapoints at all.
	tagPublisherAlarmPeriodSeconds = 300
	tagPublisherAlarmPeriods       = 1

	// tagPublisherAssetBucketEnvVar names the bucket holding the S3 copy of every
	// build's tag clock, which this function writes and (from ocelhq-wvag.5) the
	// origin reads. tagPublisherWriterParamEnvVar and
	// tagPublisherSeedParamEnvVar name the two SSM parameters it derives a
	// build's write secret from: the writer's endpoint, and the substrate's
	// write-secret seed. The artifact refuses to start without all three.
	tagPublisherAssetBucketEnvVar = "OCEL_ASSET_BUCKET"
	tagPublisherWriterParamEnvVar = "OCEL_ISR_WRITER_PARAM"
	tagPublisherSeedParamEnvVar   = "OCEL_ISR_WRITER_SEED_PARAM"
)

// tagPublisherFilter is the event source mapping's filter, and it is a security
// boundary rather than a cost saving.
//
// The stream carries the whole table. Upload sessions live there under the very
// same sort key (cloud/aws/runtime/bucket/store.go) and carry HMAC secrets, so a
// filter on `sk = "#META"` alone — which is what this work was originally
// specified as — would stream credential material to a function that has no
// business seeing it. The pk prefix is what confines it to the tag partitions.
//
// `prefix` is a documented Lambda filter operator and DynamoDB sources exclude
// only the numeric operators (numbers are stringified in the event, which is
// also why no filter can be written against a watermark). The consumer verifies
// the namespace again in code regardless: a filter is configuration, and the one
// thing this function must never do is act on an item that is not a tag record.
const tagPublisherFilter = `{"dynamodb":{"Keys":{"pk":{"S":[{"prefix":"TAG#"}]},"sk":{"S":["#META"]}}}}`

func pinnedTagPublisher() artifactPin {
	return artifactPin{version: TagPublisherArtifactVersion, sha256: TagPublisherArtifactSHA256}
}

// tagPublisherReleaseURL is where the pinned asset is published. The tag is the
// contract the release step must satisfy; nothing discovers it.
func tagPublisherReleaseURL(version string) string {
	return fmt.Sprintf("https://github.com/ocelhq/ocel/releases/download/tag-publisher-v%s/%s", version, tagPublisherAssetName)
}

// tagPublisherArtifactKey is content-addressed on the pinned digest, so a digest
// that moves lands at a new key and CloudFormation sees a code change.
func tagPublisherArtifactKey(p artifactPin) string {
	return fmt.Sprintf("%s/%s-%s.zip", tagPublisherKeyPrefix, p.version, p.digest())
}

// ensureTagPublisherArtifact places the pinned publisher zip; see ensureArtifact
// for the fail-closed discipline it runs under.
func ensureTagPublisherArtifact(ctx context.Context, art Artifacts, bucket string, p artifactPin) (artifactCode, error) {
	return ensureArtifact(ctx, art, bucket, tagPublisherArtifactKey(p), tagPublisherReleaseURL(p.version), tagPublisherLabel, p)
}

// tagPublisherResources renders the publisher's dead-letter queue, execution
// role, function, event source mapping and DLQ-depth alarm — or nothing when no
// artifact is available, which leaves the substrate publishing exactly the way
// it did before this function existed.
//
// The role is scoped to what the function actually does and nothing else: read
// the one stream, write the tag-clock objects in this substrate's own asset
// bucket, read the two SSM parameters it derives a build's write secret from,
// and send to its own queue. The bucket grant is not narrower than the bucket
// because the objects are keyed by a prefix the function learns at runtime, one
// per build, and there is no prefix shape it could be pinned to that every
// build's key does not already share.
//
// Like the optimizer, the function is deliberately unnamed — CloudFormation
// autonames it and every grant is by !GetAtt — and nothing tags it ocel:app: it
// belongs to no app, and a fabricated tag would both be a lie and misclassify it
// for everything keyed off that tag.
//
// The alarm is on the DLQ's depth rather than on publisher liveness. A true
// heartbeat-staleness alarm needs a metric nothing in this account emits yet;
// ocelhq-wvag.13 invents it, and must land before ocelhq-wvag.6 makes this
// function the fleet's sole guarantor of on-demand invalidation.
func tagPublisherResources(code artifactCode, class string) string {
	if !code.present() {
		return ""
	}
	writerParam, seedParam := isrWriterParamNames(class)
	return fmt.Sprintf(`  TagPublisherDeadLetterQueue:
    Type: AWS::SQS::Queue
    Properties:
      MessageRetentionPeriod: %d
      SqsManagedSseEnabled: true
  TagPublisherRole:
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
      Description: Ocel tag-snapshot publisher - the single writer of every build's tag clock in this substrate.
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
    Properties:
      EventSourceArn: !GetAtt StateTable.StreamArn
      FunctionName: !GetAtt TagPublisher.Arn
      StartingPosition: LATEST
      BatchSize: %d
      MaximumBatchingWindowInSeconds: 0
      BisectBatchOnFunctionError: true
      MaximumRetryAttempts: %d
      DestinationConfig:
        OnFailure:
          Destination: !GetAtt TagPublisherDeadLetterQueue.Arn
      FilterCriteria:
        Filters:
          - Pattern: '%s'
  TagPublisherDeadLetterAlarm:
    Type: AWS::CloudWatch::Alarm
    Properties:
      AlarmDescription: A tag-snapshot batch exhausted its retries; those builds' invalidations were dropped and their edge replicas are behind.
      Namespace: AWS/SQS
      MetricName: ApproximateNumberOfMessagesVisible
      Dimensions:
        - Name: QueueName
          Value: !GetAtt TagPublisherDeadLetterQueue.QueueName
      Statistic: Maximum
      Period: %d
      EvaluationPeriods: %d
      Threshold: 0
      ComparisonOperator: GreaterThanThreshold
      TreatMissingData: notBreaching
`, tagPublisherDLQRetentionSeconds,
		writerParam, seedParam, writerParam, seedParam,
		tagPublisherRuntime, tagPublisherArchitecture, tagPublisherHandler, tagPublisherMemoryMB, tagPublisherTimeoutSeconds,
		code.bucket, code.key,
		tagPublisherAssetBucketEnvVar, tagPublisherWriterParamEnvVar, writerParam, tagPublisherSeedParamEnvVar, seedParam,
		tagPublisherBatchSize, tagPublisherRetries, tagPublisherFilter,
		tagPublisherAlarmPeriodSeconds, tagPublisherAlarmPeriods)
}

// isrWriterParamNames is the pair of SSM parameters the publisher reads. Both
// callers are the substrate templates, each passing its own class constant, so
// there is no unknown class to report — and an empty pair would render a
// parameter ARN CloudFormation rejects rather than a grant that quietly works.
func isrWriterParamNames(class string) (writer, seed string) {
	names := edgeNamesByClass[class]
	return names.isrWriterParam, names.isrWriterSeedParam
}
