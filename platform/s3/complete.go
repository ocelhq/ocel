package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

type signedFile struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}

type canonicalPayload struct {
	SessionID string     `json:"sessionId"`
	File      signedFile `json:"file"`
}

func canonicalUploadPayload(sessionID string, file signedFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(canonicalPayload{SessionID: sessionID, File: file}); err != nil {
		return nil, fmt.Errorf("encode the canonical upload payload: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func signUpload(secret, sessionID string, file signedFile) (string, error) {
	payload, err := canonicalUploadPayload(sessionID, file)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func verifyUpload(secret, sessionID string, file signedFile, signature string) (bool, error) {
	expected, err := signUpload(secret, sessionID, file)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(expected), []byte(signature)), nil
}

type callbackBody struct {
	SessionID string     `json:"sessionId"`
	Signature string     `json:"signature"`
	File      signedFile `json:"file"`
}

func (s *Service) CompleteUpload(ctx context.Context, req *bucketv1.CompleteUploadRequest) (*bucketv1.CompleteUploadResponse, error) {
	sess, err := s.anySession(ctx, req.GetSessionId())
	if errors.Is(err, errSessionNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return nil, err
	}

	granted, err := s.scopeOf(sess.Bucket)
	if err != nil {
		return nil, err
	}
	switch aggregate(sess.Files) {
	case stateSucceeded:
		return s.finish(ctx, sess)
	case stateExpired:
		return &bucketv1.CompleteUploadResponse{State: bucketv1.UploadState_UPLOAD_STATE_EXPIRED, Error: sess.Error}, nil
	}

	if s.now().Unix() >= sess.ExpiresAt {
		return s.expire(ctx, sess, granted)
	}

	files := make([]sessionFile, len(sess.Files))
	copy(files, sess.Files)
	var rejected []string
	failure := ""

	for i, file := range files {
		if file.State == stateSucceeded {
			continue
		}
		out, err := s.cfg.Objects.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(granted.bucket),
			Key:    aws.String(granted.key(file.Key)),
		})
		if missing(err) {
			continue
		}
		if err != nil {
			return nil, storeError("head "+file.Key, err)
		}
		if aws.ToInt64(out.ContentLength) != file.Size || aws.ToString(out.ContentType) != file.MimeType {
			rejected = append(rejected, file.Key)
			failure = fmt.Sprintf("the object uploaded for %q does not match what was signed", file.Key)
			continue
		}
		files[i].State = stateSucceeded
	}

	if len(rejected) > 0 {
		if err := s.remove(ctx, granted, rejected); err != nil {
			return nil, err
		}
		sess.Files = files
		sess.Error = failure
		for i := range sess.Files {
			sess.Files[i].State = stateExpired
		}
		if err := s.close(ctx, &sess); err != nil {
			return nil, err
		}
		return &bucketv1.CompleteUploadResponse{State: bucketv1.UploadState_UPLOAD_STATE_EXPIRED, Error: failure}, nil
	}

	sess.Files = files
	if aggregate(files) != stateSucceeded {
		return &bucketv1.CompleteUploadResponse{State: bucketv1.UploadState_UPLOAD_STATE_PENDING}, nil
	}
	return s.finish(ctx, sess)
}

func (s *Service) finish(ctx context.Context, sess session) (*bucketv1.CompleteUploadResponse, error) {
	succeeded := &bucketv1.CompleteUploadResponse{State: bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED}

	claimed := make([]sessionFile, 0, len(sess.Files))
	for i, file := range sess.Files {
		if !file.Notified {
			claimed = append(claimed, file)
			sess.Files[i].Notified = true
		}
	}
	if len(claimed) == 0 {
		return succeeded, nil
	}
	if err := s.close(ctx, &sess); err != nil {
		if errors.Is(err, errSessionMoved) {
			return succeeded, nil
		}
		return nil, err
	}
	delivered, err := s.notify(ctx, sess, claimed)
	if err != nil {
		return nil, errors.Join(err, s.disown(ctx, sess, claimed[delivered:]))
	}
	return succeeded, nil
}

func (s *Service) disown(ctx context.Context, sess session, lost []sessionFile) error {
	if len(lost) == 0 {
		return nil
	}
	for i := range sess.Files {
		for _, file := range lost {
			if sess.Files[i].Key == file.Key {
				sess.Files[i].Notified = false
			}
		}
	}
	if err := s.close(ctx, &sess); err != nil && !errors.Is(err, errSessionMoved) {
		return err
	}
	return nil
}

func (s *Service) expire(ctx context.Context, sess session, granted scope) (*bucketv1.CompleteUploadResponse, error) {
	var unconfirmed []string
	for _, file := range sess.Files {
		if file.State != stateSucceeded {
			unconfirmed = append(unconfirmed, file.Key)
		}
	}
	if err := s.remove(ctx, granted, unconfirmed); err != nil {
		return nil, err
	}
	for i := range sess.Files {
		sess.Files[i].State = stateExpired
	}
	sess.Error = "upload expired"
	if err := s.close(ctx, &sess); err != nil && !errors.Is(err, errSessionMoved) {
		return nil, err
	}
	return &bucketv1.CompleteUploadResponse{
		State: bucketv1.UploadState_UPLOAD_STATE_EXPIRED,
		Error: "upload expired",
	}, nil
}

func (s *Service) close(ctx context.Context, sess *session) error {
	return s.writeSession(ctx, sess)
}

func (s *Service) notify(ctx context.Context, sess session, files []sessionFile) (int, error) {
	if s.cfg.Callbacks == nil || sess.CallbackBaseURL == "" {
		return len(files), nil
	}
	for delivered, file := range files {
		payload := signedFile{Key: file.Key, Name: file.Name, Size: file.Size, MimeType: file.MimeType}
		signature, err := signUpload(sess.Secret, sess.SessionID, payload)
		if err != nil {
			return delivered, connect.NewError(connect.CodeInternal, err)
		}
		body, err := json.Marshal(callbackBody{SessionID: sess.SessionID, Signature: signature, File: payload})
		if err != nil {
			return delivered, connect.NewError(connect.CodeInternal, fmt.Errorf("encode the upload callback: %w", err))
		}
		if err := s.cfg.Callbacks.Post(ctx, sess.CallbackBaseURL+"?op=callback", body); err != nil {
			return delivered, connect.NewError(connect.CodeInternal, fmt.Errorf("deliver the upload callback: %w", err))
		}
	}
	return len(files), nil
}
