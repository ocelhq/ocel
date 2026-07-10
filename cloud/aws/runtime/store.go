package runtime

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
// the metadata.
//
// T7 provisions a table with exactly this shape: partition key session_id (S),
// TTL attribute expires_at (N).
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
