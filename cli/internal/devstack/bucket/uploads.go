package bucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	production "github.com/ocelhq/ocel/platform/aws/runtime/bucket"
)

const (
	uploadQueue         = "ocel-dev-uploads"
	redeliverAfter      = "5"
	forgetAfter         = "3600"
	longPollSeconds     = 10
	firstReceiveBackoff = time.Second
	maxReceiveBackoff   = 30 * time.Second
	completionAttempts  = 3
)

func ensureSessionTable(ctx context.Context, ddb *dynamodb.Client) error {
	_, err := ddb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(sessionTable),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: ddbtypes.KeyTypeRange},
		},
	})
	var exists *ddbtypes.ResourceInUseException
	if err != nil && !errors.As(err, &exists) {
		return fmt.Errorf("create the upload session table: %w", err)
	}
	return nil
}

type uploads struct {
	queueURL string
	queueARN string
	cancel   context.CancelFunc
	done     chan struct{}

	mu         sync.Mutex
	completers map[string]completing
	failed     map[string]int
}

type completing struct {
	completer *production.UploadCompleter
	origins   []string
}

func watchUploads(ctx context.Context, queue *sqs.Client, report func(error)) (*uploads, error) {
	created, err := queue.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(uploadQueue),
		Attributes: map[string]string{
			string(sqstypes.QueueAttributeNameVisibilityTimeout):      redeliverAfter,
			string(sqstypes.QueueAttributeNameMessageRetentionPeriod): forgetAfter,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create the upload notification queue: %w", err)
	}
	described, err := queue.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       created.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return nil, fmt.Errorf("read the upload notification queue's arn: %w", err)
	}

	watching, cancel := context.WithCancel(context.WithoutCancel(ctx))
	u := &uploads{
		queueURL:   aws.ToString(created.QueueUrl),
		queueARN:   described.Attributes[string(sqstypes.QueueAttributeNameQueueArn)],
		cancel:     cancel,
		done:       make(chan struct{}),
		completers: map[string]completing{},
		failed:     map[string]int{},
	}
	go u.watch(watching, queue, report)
	return u, nil
}

func (u *uploads) stop() {
	u.cancel()
	<-u.done
}

func (u *uploads) watch(ctx context.Context, queue *sqs.Client, report func(error)) {
	defer close(u.done)

	backoff := firstReceiveBackoff
	for ctx.Err() == nil {
		received, err := queue.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(u.queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     longPollSeconds,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			report(fmt.Errorf("read upload notifications: %w", err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxReceiveBackoff)
			continue
		}
		backoff = firstReceiveBackoff

		for _, message := range received.Messages {
			if err := u.complete(ctx, aws.ToString(message.Body)); err != nil {
				if ctx.Err() != nil || !u.givesUpOn(aws.ToString(message.MessageId)) {
					continue
				}
				report(fmt.Errorf("complete an upload, given up on after %d attempts: %w", completionAttempts, err))
			}
			if _, err := queue.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(u.queueURL), ReceiptHandle: message.ReceiptHandle}); err != nil && ctx.Err() == nil {
				report(fmt.Errorf("acknowledge an upload notification: %w", err))
			}
		}
	}
}

func (u *uploads) givesUpOn(messageID string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failed[messageID]++
	if u.failed[messageID] < completionAttempts {
		return false
	}
	delete(u.failed, messageID)
	return true
}

func (u *uploads) serves(name string, origins []string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	completer, provisioned := u.completers[name]
	return provisioned && slices.Equal(completer.origins, origins)
}

func (u *uploads) granted() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	names := make([]string, 0, len(u.completers))
	for name := range u.completers {
		names = append(names, name)
	}
	return names
}

func (u *uploads) serve(name string, completer completing) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.completers[name] = completer
}

func (u *uploads) complete(ctx context.Context, body string) error {
	var event production.S3Event
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return nil
	}
	for _, record := range event.Records {
		u.mu.Lock()
		completer, known := u.completers[record.S3.Bucket.Name]
		u.mu.Unlock()
		if !known {
			continue
		}
		if err := completer.completer.Handle(ctx, production.S3Event{Records: []production.S3EventRecord{record}}); err != nil {
			return err
		}
	}
	return nil
}

func (e *emulator) provision(ctx context.Context, name string, origins []string) error {
	if e.uploads.serves(name, origins) {
		return nil
	}

	_, err := e.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
	var owned *s3types.BucketAlreadyOwnedByYou
	var taken *s3types.BucketAlreadyExists
	if err != nil && !errors.As(err, &owned) && !errors.As(err, &taken) {
		return fmt.Errorf("create it in the emulator: %w", err)
	}
	if len(origins) > 0 {
		_, err = e.s3.PutBucketCors(ctx, &s3.PutBucketCorsInput{
			Bucket: aws.String(name),
			CORSConfiguration: &s3types.CORSConfiguration{CORSRules: []s3types.CORSRule{{
				AllowedOrigins: origins,
				AllowedMethods: []string{"PUT"},
				AllowedHeaders: []string{"*"},
				ExposeHeaders:  []string{"ETag"},
			}}},
		})
		if err != nil {
			return fmt.Errorf("allow its origins to upload: %w", err)
		}
	}
	_, err = e.s3.PutBucketNotificationConfiguration(ctx, &s3.PutBucketNotificationConfigurationInput{
		Bucket: aws.String(name),
		NotificationConfiguration: &s3types.NotificationConfiguration{QueueConfigurations: []s3types.QueueConfiguration{{
			QueueArn: aws.String(e.uploads.queueARN),
			Events:   []s3types.Event{s3types.EventS3ObjectCreated},
		}}},
	})
	if err != nil {
		return fmt.Errorf("have it announce finished uploads: %w", err)
	}

	e.uploads.serve(name, completing{origins: origins, completer: production.NewUploadCompleter(production.UploadCompleterConfig{
		DDB:              e.ddb,
		Tagger:           e.s3,
		Table:            sessionTable,
		SessionKeyPrefix: sessionPrefix,
		AllowedOrigins:   origins,
	})})
	return nil
}
