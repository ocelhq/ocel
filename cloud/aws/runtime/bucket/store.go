package bucket

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fileState is one file's lifecycle within a session. It mirrors the proto
// UploadState set and the dev store's FileState.
type fileState string

const (
	statePending   fileState = "pending"
	stateSucceeded fileState = "succeeded"
	stateExpired   fileState = "expired"
)

// sessionFile is one file's persisted record inside a session. key is the
// user's object key as-is (prod has no tenancy prefix — the env owns its
// bucket).
type sessionFile struct {
	Key      string    `dynamodbav:"key"`
	Name     string    `dynamodbav:"name"`
	Size     int64     `dynamodbav:"size"`
	MimeType string    `dynamodbav:"mime_type"`
	State    fileState `dynamodbav:"state"`
	Error    string    `dynamodbav:"error,omitempty"`
}

// session is the DynamoDB item persisted at presign. The table's partition key
// is session_id (no sort key); expires_at is the table's TTL attribute (epoch
// seconds) so DynamoDB reaps orphaned sessions. The secret never leaves this
// item — VerifyUploadSignature re-derives the HMAC store-side and returns only
// the metadata. The provisioned table must match this shape exactly.
type session struct {
	SessionID          string        `dynamodbav:"session_id"`
	Secret             string        `dynamodbav:"secret"`
	Bucket             string        `dynamodbav:"bucket"`
	CallbackBaseURL    string        `dynamodbav:"callback_base_url"`
	ContentDisposition string        `dynamodbav:"content_disposition,omitempty"`
	Metadata           []byte        `dynamodbav:"metadata"`
	Files              []sessionFile `dynamodbav:"files"`
	CreatedAt          int64         `dynamodbav:"created_at"`
	ExpiresAt          int64         `dynamodbav:"expires_at"`
}

// errSessionNotFound is returned when no session item exists for an id.
var errSessionNotFound = errors.New("session not found")

// ddbAPI is the subset of the DynamoDB client the store uses, narrowed so tests
// can substitute a fake without a live table.
type ddbAPI interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

// sessionStore persists and reads upload sessions in a single DynamoDB table.
type sessionStore struct {
	client ddbAPI
	table  string
}

func (s *sessionStore) put(ctx context.Context, sess session) error {
	item, err := attributevalue.MarshalMap(sess)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("put session: %w", err)
	}
	return nil
}

func (s *sessionStore) get(ctx context.Context, sessionID string) (session, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.table),
		Key: map[string]ddbtypes.AttributeValue{
			"session_id": &ddbtypes.AttributeValueMemberS{Value: sessionID},
		},
	})
	if err != nil {
		return session{}, fmt.Errorf("get session: %w", err)
	}
	if len(out.Item) == 0 {
		return session{}, errSessionNotFound
	}
	var sess session
	if err := attributevalue.UnmarshalMap(out.Item, &sess); err != nil {
		return session{}, fmt.Errorf("unmarshal session: %w", err)
	}
	return sess, nil
}

// markSucceeded atomically transitions the file at index idx of sessionID from
// pending to succeeded, guarded on it still being pending. This conditional
// UpdateItem is the single point of idempotency for prod completion: duplicate
// S3 deliveries race on the item, and only the delivery that observes state =
// pending at write time transitions and reports transitioned = true. A guard
// failure (already non-pending) is not an error — it returns transitioned =
// false so the caller no-ops without firing the route callback. Mirrors the dev
// detector's guarded UPDATE (packages/api/src/routes/blob/detect/route.ts).
func (s *sessionStore) markSucceeded(ctx context.Context, sessionID string, idx int) (bool, error) {
	expr := fmt.Sprintf("files[%d].#st", idx)
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.table),
		Key: map[string]ddbtypes.AttributeValue{
			"session_id": &ddbtypes.AttributeValueMemberS{Value: sessionID},
		},
		UpdateExpression:         aws.String("SET " + expr + " = :succeeded"),
		ConditionExpression:      aws.String(expr + " = :pending"),
		ExpressionAttributeNames: map[string]string{"#st": "state"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":succeeded": &ddbtypes.AttributeValueMemberS{Value: string(stateSucceeded)},
			":pending":   &ddbtypes.AttributeValueMemberS{Value: string(statePending)},
		},
	})
	if err != nil {
		var ccf *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &ccf) {
			return false, nil
		}
		return false, fmt.Errorf("transition session %s file %d: %w", sessionID, idx, err)
	}
	return true, nil
}

// aggregateState collapses per-file states into the session-level state op=poll
// reports: any expired file makes the session terminally expired; otherwise it
// is succeeded only once every file has succeeded; else pending. Mirrors the
// dev store's aggregateState.
func aggregateState(files []sessionFile) fileState {
	if len(files) == 0 {
		return statePending
	}
	allSucceeded := true
	for _, f := range files {
		if f.State == stateExpired {
			return stateExpired
		}
		if f.State != stateSucceeded {
			allSucceeded = false
		}
	}
	if allSucceeded {
		return stateSucceeded
	}
	return statePending
}
