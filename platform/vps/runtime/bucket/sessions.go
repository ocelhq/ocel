package bucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fileState string

const (
	statePending   fileState = "pending"
	stateSucceeded fileState = "succeeded"
	stateExpired   fileState = "expired"
)

type sessionFile struct {
	Key      string    `json:"key"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	MimeType string    `json:"mimeType"`
	State    fileState `json:"state"`
	Notified bool      `json:"notified"`
}

type session struct {
	SessionID          string        `json:"sessionId"`
	Secret             string        `json:"secret"`
	Bucket             string        `json:"bucket"`
	CallbackBaseURL    string        `json:"callbackBaseUrl"`
	ContentDisposition string        `json:"contentDisposition,omitempty"`
	Metadata           []byte        `json:"metadata,omitempty"`
	Files              []sessionFile `json:"files"`
	CreatedAt          int64         `json:"createdAt"`
	ExpiresAt          int64         `json:"expiresAt"`
	Error              string        `json:"error,omitempty"`

	etag  string
	scope scope
}

var errSessionNotFound = errors.New("session not found")

func sessionKey(id string) string {
	return sessionPrefix + id
}

func (s *Service) createSession(ctx context.Context, sess session) error {
	held := s.sessions
	body, err := json.Marshal(sess)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("encode the upload session: %w", err))
	}
	_, err = s.cfg.Objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(held.bucket),
		Key:         aws.String(held.key(sessionKey(sess.SessionID))),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("open the upload session: %w", err))
	}
	return nil
}

func (s *Service) readSession(ctx context.Context, held scope, id string) (session, error) {
	out, err := s.cfg.Objects.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(held.bucket),
		Key:    aws.String(held.key(sessionKey(id))),
	})
	if missing(err) {
		return session{}, errSessionNotFound
	}
	if err != nil {
		return session{}, connect.NewError(connect.CodeInternal, fmt.Errorf("read the upload session: %w", err))
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return session{}, connect.NewError(connect.CodeInternal, fmt.Errorf("read the upload session: %w", err))
	}
	var sess session
	if err := json.Unmarshal(body, &sess); err != nil {
		return session{}, connect.NewError(connect.CodeInternal, fmt.Errorf("decode the upload session: %w", err))
	}
	sess.etag = aws.ToString(out.ETag)
	sess.scope = held
	return sess, nil
}

func (s *Service) anySession(ctx context.Context, id string) (session, error) {
	return s.readSession(ctx, s.sessions, id)
}

func (s *Service) writeSession(ctx context.Context, sess *session) error {
	body, err := json.Marshal(sess)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("encode the upload session: %w", err))
	}
	out, err := s.cfg.Objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(sess.scope.bucket),
		Key:         aws.String(sess.scope.key(sessionKey(sess.SessionID))),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
		IfMatch:     aws.String(sess.etag),
	})
	if preconditionFailed(err) {
		return errSessionMoved
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("settle the upload session: %w", err))
	}
	sess.etag = aws.ToString(out.ETag)
	return nil
}

var errSessionMoved = errors.New("another replica settled this session first")

func aggregate(files []sessionFile) fileState {
	if len(files) == 0 {
		return statePending
	}
	for _, f := range files {
		if f.State == stateExpired {
			return stateExpired
		}
	}
	for _, f := range files {
		if f.State != stateSucceeded {
			return statePending
		}
	}
	return stateSucceeded
}
