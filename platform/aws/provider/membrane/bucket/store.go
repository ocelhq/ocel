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

type fileState string

const (
	statePending   fileState = "pending"
	stateSucceeded fileState = "succeeded"
	stateExpired   fileState = "expired"
)

type sessionFile struct {
	Key      string    `dynamodbav:"key"`
	Name     string    `dynamodbav:"name"`
	Size     int64     `dynamodbav:"size"`
	MimeType string    `dynamodbav:"mime_type"`
	State    fileState `dynamodbav:"state"`
	Error    string    `dynamodbav:"error,omitempty"`
}

type session struct {
	PK                 string        `dynamodbav:"pk"`
	SK                 string        `dynamodbav:"sk"`
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

var errSessionNotFound = errors.New("session not found")

type ddbAPI interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type sessionStore struct {
	client ddbAPI
	table  string
}

const (
	sessionPKPrefix = "SESSION#"
	sessionSK       = "#META"
)

func sessionKey(sessionID string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"pk": &ddbtypes.AttributeValueMemberS{Value: sessionPKPrefix + sessionID},
		"sk": &ddbtypes.AttributeValueMemberS{Value: sessionSK},
	}
}

func (s *sessionStore) put(ctx context.Context, sess session) error {
	sess.PK = sessionPKPrefix + sess.SessionID
	sess.SK = sessionSK
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
		Key:       sessionKey(sessionID),
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

func (s *sessionStore) markSucceeded(ctx context.Context, sessionID string, idx int) (bool, error) {
	expr := fmt.Sprintf("files[%d].#st", idx)
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                aws.String(s.table),
		Key:                      sessionKey(sessionID),
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
