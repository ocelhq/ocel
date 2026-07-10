package devserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	runtimev1 "github.com/ocelhq/ocel/pkg/proto/runtime/v1"
)

// runtimeShim is the dev implementation of runtime.v1.RuntimeService. It owns
// no cloud mechanics itself: on PresignUpload it forwards to the Ocel API's
// presign endpoint (authenticated with the leader's user token + projectID),
// honoring the invariant that the CLI never talks to the cloud store directly.
// The real T2 SDK dials this over Connect at the injected dev server address.
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
	URL  string `json:"url"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type presignResponseBody struct {
	SessionID string            `json:"sessionId"`
	Files     []presignedTarget `json:"files"`
}

// PresignUpload forwards the SDK's presign request to the Ocel API and returns
// its response verbatim. The API derives (org, project, user) from the token,
// prepends the tenancy prefix, persists the pending session, and mints the
// presigned targets.
func (s *runtimeShim) PresignUpload(ctx context.Context, req *runtimev1.PresignUploadRequest) (*runtimev1.PresignUploadResponse, error) {
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.apiURL, "/")+"/api/blob/presign", bytes.NewReader(body))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build presign request: %w", err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.token)
	}

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

	targets := make([]*runtimev1.PresignedTarget, 0, len(decoded.Files))
	for _, t := range decoded.Files {
		targets = append(targets, &runtimev1.PresignedTarget{Url: t.URL, Key: t.Key, Name: t.Name})
	}

	return &runtimev1.PresignUploadResponse{SessionId: decoded.SessionID, Files: targets}, nil
}

// VerifyUploadSignature is deferred to T4 (dev completion detection). Left
// unimplemented so a caller reaching it in this slice fails loudly rather than
// silently mis-verifying.
func (s *runtimeShim) VerifyUploadSignature(context.Context, *runtimev1.VerifyUploadSignatureRequest) (*runtimev1.VerifyUploadSignatureResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("runtime.v1.RuntimeService.VerifyUploadSignature is deferred to ocel/blob T4"))
}

// GetUploadStatus is deferred to T4 (dev completion detection), for the same
// reason as VerifyUploadSignature.
func (s *runtimeShim) GetUploadStatus(context.Context, *runtimev1.GetUploadStatusRequest) (*runtimev1.GetUploadStatusResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("runtime.v1.RuntimeService.GetUploadStatus is deferred to ocel/blob T4"))
}
