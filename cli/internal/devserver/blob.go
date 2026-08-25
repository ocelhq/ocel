package devserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
)

type blobProxy struct {
	apiURL     string
	token      string
	projectID  string
	httpClient *http.Client
}

func newBlobProxy(apiURL, token, projectID string) *blobProxy {
	return &blobProxy{
		apiURL:     apiURL,
		token:      token,
		projectID:  projectID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type presignFile struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}

type presignRequestBody struct {
	ProjectID          string        `json:"projectId"`
	Bucket             string        `json:"bucket"`
	Metadata           []byte        `json:"metadata"`
	Files              []presignFile `json:"files"`
	ContentDisposition string        `json:"contentDisposition"`
	CallbackBaseURL    string        `json:"callbackBaseUrl"`
}

type presignedTarget struct {
	URL                string `json:"url"`
	Key                string `json:"key"`
	Name               string `json:"name"`
	ContentDisposition string `json:"contentDisposition"`
}

type presignResponseBody struct {
	SessionID string            `json:"sessionId"`
	Files     []presignedTarget `json:"files"`
}

func (s *blobProxy) PresignUpload(ctx context.Context, req *blobv1.PresignUploadRequest) (*blobv1.PresignUploadResponse, error) {
	files := make([]presignFile, 0, len(req.GetFiles()))
	for _, f := range req.GetFiles() {
		files = append(files, presignFile{
			Key:      f.GetKey(),
			Name:     f.GetName(),
			Size:     f.GetSize(),
			MimeType: f.GetMimeType(),
		})
	}

	body, err := json.Marshal(presignRequestBody{
		ProjectID:          s.projectID,
		Bucket:             req.GetBucket(),
		Metadata:           req.GetMetadata(),
		Files:              files,
		ContentDisposition: req.GetContentDisposition(),
		CallbackBaseURL:    req.GetCallbackBaseUrl(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode presign request: %w", err))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiEndpoint("/api/blob/presign"), bytes.NewReader(body))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build presign request: %w", err))
	}
	s.authorize(httpReq)

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("presign upload: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("presign upload: unexpected status %d", resp.StatusCode))
	}

	var decoded presignResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode presign response: %w", err))
	}

	targets := make([]*blobv1.PresignedTarget, 0, len(decoded.Files))
	for _, t := range decoded.Files {
		targets = append(targets, &blobv1.PresignedTarget{Url: t.URL, Key: t.Key, Name: t.Name, ContentDisposition: t.ContentDisposition})
	}

	return &blobv1.PresignUploadResponse{SessionId: decoded.SessionID, Files: targets}, nil
}

type signedCompletion struct {
	SessionID string        `json:"sessionId"`
	Signature string        `json:"signature"`
	File      completedFile `json:"file"`
}

type completedFile struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}

type verifyResponseBody struct {
	Valid    bool   `json:"valid"`
	Metadata []byte `json:"metadata"`
}

func (s *blobProxy) VerifyUploadSignature(ctx context.Context, req *blobv1.VerifyUploadSignatureRequest) (*blobv1.VerifyUploadSignatureResponse, error) {
	f := req.GetFile()
	body, err := json.Marshal(signedCompletion{
		SessionID: req.GetSessionId(),
		Signature: req.GetSignature(),
		File: completedFile{
			Key:      f.GetKey(),
			Name:     f.GetName(),
			Size:     f.GetSize(),
			MimeType: f.GetMimeType(),
		},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode verify request: %w", err))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiEndpoint("/api/blob/verify"), bytes.NewReader(body))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build verify request: %w", err))
	}
	s.authorize(httpReq)

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("verify upload signature: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("verify upload signature: unexpected status %d", resp.StatusCode))
	}

	var decoded verifyResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode verify response: %w", err))
	}
	return &blobv1.VerifyUploadSignatureResponse{Valid: decoded.Valid, Metadata: decoded.Metadata}, nil
}

type statusResponseBody struct {
	State string `json:"state"`
	Error string `json:"error"`
}

func (s *blobProxy) GetUploadStatus(ctx context.Context, req *blobv1.GetUploadStatusRequest) (*blobv1.GetUploadStatusResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiEndpoint("/api/blob/status")+"?sessionId="+url.QueryEscape(req.GetSessionId()), nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build status request: %w", err))
	}
	s.authorize(httpReq)

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("get upload status: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get upload status: unexpected status %d", resp.StatusCode))
	}

	var decoded statusResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode status response: %w", err))
	}
	return &blobv1.GetUploadStatusResponse{State: uploadStateFromString(decoded.State), Error: decoded.Error}, nil
}

func uploadStateFromString(s string) blobv1.UploadState {
	switch s {
	case "succeeded":
		return blobv1.UploadState_UPLOAD_STATE_SUCCEEDED
	case "expired":
		return blobv1.UploadState_UPLOAD_STATE_EXPIRED
	case "pending":
		return blobv1.UploadState_UPLOAD_STATE_PENDING
	default:
		return blobv1.UploadState_UPLOAD_STATE_UNSPECIFIED
	}
}

func (s *blobProxy) apiEndpoint(path string) string {
	return endpoint(s.apiURL, path)
}

func endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

func (s *blobProxy) authorize(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
}
