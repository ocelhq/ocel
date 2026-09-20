package bucket

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

// CompleteUpload settles a session against what the store actually holds.
//
// No store this runtime drives is guaranteed to raise an event when an object
// lands, so the upload is confirmed by asking: every file the session covers is
// HEADed, and its size and content type must be the ones the target was signed
// for. An object that came back different is deleted and the session fails.
func (s *Service) CompleteUpload(ctx context.Context, req *bucketv1.CompleteUploadRequest) (*bucketv1.CompleteUploadResponse, error) {
	sess, err := s.anySession(ctx, req.GetSessionId())
	if errors.Is(err, errSessionNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return nil, err
	}

	if state := aggregate(sess.Files); state != statePending {
		return &bucketv1.CompleteUploadResponse{State: toProtoState(state), Error: sess.Error}, nil
	}

	held, err := s.held(sess.Bucket)
	if err != nil {
		return nil, err
	}
	if s.now().Unix() >= sess.ExpiresAt {
		return s.expire(ctx, sess, held)
	}

	settled := make([]sessionFile, len(sess.Files))
	copy(settled, sess.Files)
	var rejected []string
	failure := ""

	for i, file := range settled {
		if file.State == stateSucceeded {
			continue
		}
		out, err := s.cfg.Objects.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(held.bucket),
			Key:    aws.String(held.key(file.Key)),
		})
		if missing(err) {
			continue
		}
		if err != nil {
			return nil, storeError("head "+file.Key, err)
		}
		if aws.ToInt64(out.ContentLength) != file.Size || aws.ToString(out.ContentType) != file.MimeType {
			rejected = append(rejected, file.Key)
			failure = fmt.Sprintf("the object uploaded for %q is not the one that was signed for", file.Key)
			continue
		}
		settled[i].State = stateSucceeded
	}

	if len(rejected) > 0 {
		if err := s.remove(ctx, held, rejected); err != nil {
			return nil, err
		}
		sess.Files = settled
		sess.Error = failure
		for i := range sess.Files {
			sess.Files[i].State = stateExpired
		}
		if err := s.close(ctx, sess); err != nil {
			return nil, err
		}
		return &bucketv1.CompleteUploadResponse{State: bucketv1.UploadState_UPLOAD_STATE_EXPIRED, Error: failure}, nil
	}

	sess.Files = settled
	if aggregate(settled) != stateSucceeded {
		return &bucketv1.CompleteUploadResponse{State: bucketv1.UploadState_UPLOAD_STATE_PENDING}, nil
	}

	unnotified := make([]sessionFile, 0, len(settled))
	for i, file := range settled {
		if !file.Notified {
			unnotified = append(unnotified, file)
			sess.Files[i].Notified = true
		}
	}
	if err := s.close(ctx, sess); err != nil {
		if errors.Is(err, errSessionMoved) {
			return &bucketv1.CompleteUploadResponse{State: bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED}, nil
		}
		return nil, err
	}
	if err := s.notify(ctx, sess, unnotified); err != nil {
		return nil, err
	}
	return &bucketv1.CompleteUploadResponse{State: bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED}, nil
}

func (s *Service) expire(ctx context.Context, sess session, held scope) (*bucketv1.CompleteUploadResponse, error) {
	var unconfirmed []string
	for _, file := range sess.Files {
		if file.State != stateSucceeded {
			unconfirmed = append(unconfirmed, file.Key)
		}
	}
	if err := s.remove(ctx, held, unconfirmed); err != nil {
		return nil, err
	}
	for i := range sess.Files {
		sess.Files[i].State = stateExpired
	}
	sess.Error = "upload expired"
	if err := s.close(ctx, sess); err != nil && !errors.Is(err, errSessionMoved) {
		return nil, err
	}
	return &bucketv1.CompleteUploadResponse{
		State: bucketv1.UploadState_UPLOAD_STATE_EXPIRED,
		Error: "upload expired",
	}, nil
}

func (s *Service) close(ctx context.Context, sess session) error {
	return s.writeSession(ctx, sess)
}

func (s *Service) notify(ctx context.Context, sess session, files []sessionFile) error {
	if s.cfg.Callbacks == nil || sess.CallbackBaseURL == "" {
		return nil
	}
	for _, file := range files {
		payload := signedFile{Key: file.Key, Name: file.Name, Size: file.Size, MimeType: file.MimeType}
		signature, err := signUpload(sess.Secret, sess.SessionID, payload)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		body, err := json.Marshal(callbackBody{SessionID: sess.SessionID, Signature: signature, File: payload})
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("encode the upload callback: %w", err))
		}
		if err := s.cfg.Callbacks.Post(ctx, sess.CallbackBaseURL+"?op=callback", body); err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("deliver the upload callback: %w", err))
		}
	}
	return nil
}
