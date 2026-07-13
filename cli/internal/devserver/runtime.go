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

	bucketsv1 "github.com/ocelhq/ocel/pkg/proto/buckets/v1"
)

// runtimeShim is the dev implementation of runtime.v1.BucketService. It owns
// no cloud mechanics itself: on PresignUpload it forwards to the Ocel API's
// presign endpoint (authenticated with the leader's user token + projectID),
// honoring the invariant that the CLI never talks to the cloud store directly.
// The SDK dials this over Connect at the injected dev server address.
type runtimeShim struct {
	apiURL     string
	token      string
	projectID  string
	httpClient *http.Client
}

func newRuntimeShim(apiURL, token, projectID string) *runtimeShim {
	return &runtimeShim{
		apiURL:     apiURL,
		token:      token,
		projectID:  projectID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// presignFile mirrors one PresignFile on the wire to the Ocel API. size is a
// JSON number (files are single-PUT, well within a safe integer).
type presignFile struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}

type presignRequestBody struct {
	ProjectID string `json:"projectId"`
	Bucket    string `json:"bucket"`
	// Metadata is the opaque SDK-encoded bytes; encoding/json renders []byte as
	// base64, which the Ocel API stores verbatim.
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

// PresignUpload forwards the SDK's presign request to the Ocel API and returns
// its response verbatim. The API derives (org, project, user) from the token,
// prepends the tenancy prefix, persists the pending session, and mints the
// presigned targets.
func (s *runtimeShim) PresignUpload(ctx context.Context, req *bucketsv1.PresignUploadRequest) (*bucketsv1.PresignUploadResponse, error) {
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

	targets := make([]*bucketsv1.PresignedTarget, 0, len(decoded.Files))
	for _, t := range decoded.Files {
		targets = append(targets, &bucketsv1.PresignedTarget{Url: t.URL, Key: t.Key, Name: t.Name, ContentDisposition: t.ContentDisposition})
	}

	return &bucketsv1.PresignUploadResponse{SessionId: decoded.SessionID, Files: targets}, nil
}

// signedCompletion is the {sessionId, signature, file} payload a completion
// callback carries and VerifyUploadSignature checks - the request body for both
// the app route's op=callback and the Ocel API's /api/blob/verify.
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

// verifyResponseBody mirrors POST /api/blob/verify's response. Metadata is
// JSON-base64 on the wire; the API returns the session's verbatim stored
// metadata bytes, which decode straight into the proto's bytes field.
type verifyResponseBody struct {
	Valid    bool   `json:"valid"`
	Metadata []byte `json:"metadata"`
}

// VerifyUploadSignature forwards the route's completion-signature check to the
// Ocel API, which re-derives the per-session HMAC and constant-time compares.
// The secret never leaves the API; on a valid signature the stored metadata
// rides back here and out to the env-blind route.
func (s *runtimeShim) VerifyUploadSignature(ctx context.Context, req *bucketsv1.VerifyUploadSignatureRequest) (*bucketsv1.VerifyUploadSignatureResponse, error) {
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
	return &bucketsv1.VerifyUploadSignatureResponse{Valid: decoded.Valid, Metadata: decoded.Metadata}, nil
}

type statusResponseBody struct {
	State string `json:"state"`
	Error string `json:"error"`
}

// GetUploadStatus forwards op=poll to the Ocel API, which reads the shared
// store and aggregates the per-file states into one session state.
func (s *runtimeShim) GetUploadStatus(ctx context.Context, req *bucketsv1.GetUploadStatusRequest) (*bucketsv1.GetUploadStatusResponse, error) {
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
	return &bucketsv1.GetUploadStatusResponse{State: uploadStateFromString(decoded.State), Error: decoded.Error}, nil
}

func uploadStateFromString(s string) bucketsv1.UploadState {
	switch s {
	case "succeeded":
		return bucketsv1.UploadState_UPLOAD_STATE_SUCCEEDED
	case "expired":
		return bucketsv1.UploadState_UPLOAD_STATE_EXPIRED
	case "pending":
		return bucketsv1.UploadState_UPLOAD_STATE_PENDING
	default:
		return bucketsv1.UploadState_UPLOAD_STATE_UNSPECIFIED
	}
}

// apiEndpoint joins the configured Ocel API base URL with an absolute path.
func (s *runtimeShim) apiEndpoint(path string) string {
	return endpoint(s.apiURL, path)
}

// endpoint joins a base URL (trailing slash trimmed) with an absolute path.
func endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// authorize sets the JSON content type and the leader's bearer token on an
// Ocel API request.
func (s *runtimeShim) authorize(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
}
