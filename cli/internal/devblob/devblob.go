package devblob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"

	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
)

const UploadPath = "/blob/upload"

const sessionTTL = 2 * time.Hour

type file struct {
	key       string
	name      string
	size      int64
	mimeType  string
	succeeded bool
}

type session struct {
	id              string
	secret          []byte
	metadata        []byte
	callbackBaseURL string
	expiresAt       time.Time
	files           []*file
}

type ticket struct {
	session *session
	file    *file
}

type Store struct {
	dir     string
	baseURL string
	client  *http.Client
	now     func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	tickets  map[string]ticket
}

func New(dir, baseURL string) *Store {
	return &Store{
		dir:      dir,
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   &http.Client{Timeout: 30 * time.Second},
		now:      time.Now,
		sessions: map[string]*session{},
		tickets:  map[string]ticket{},
	}
}

func (s *Store) Routes(mux *http.ServeMux) {
	mux.HandleFunc(UploadPath, s.handlePut)
}

func token() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (s *Store) PresignUpload(_ context.Context, req *blobv1.PresignUploadRequest) (*blobv1.PresignUploadResponse, error) {
	id, err := token()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("name an upload session: %w", err))
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign an upload session: %w", err))
	}

	held := &session{
		id:              id,
		secret:          secret,
		metadata:        req.GetMetadata(),
		callbackBaseURL: req.GetCallbackBaseUrl(),
		expiresAt:       s.now().Add(sessionTTL),
	}
	targets := make([]*blobv1.PresignedTarget, 0, len(req.GetFiles()))
	tickets := make(map[string]ticket, len(req.GetFiles()))
	for _, asked := range req.GetFiles() {
		if _, err := s.pathOf(asked.GetKey()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		one := &file{key: asked.GetKey(), name: asked.GetName(), size: asked.GetSize(), mimeType: asked.GetMimeType()}
		held.files = append(held.files, one)

		pass, err := token()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("name an upload target: %w", err))
		}
		tickets[pass] = ticket{session: held, file: one}
		targets = append(targets, &blobv1.PresignedTarget{
			Url:                s.baseURL + UploadPath + "?ticket=" + pass,
			Key:                one.key,
			Name:               one.name,
			ContentDisposition: req.GetContentDisposition(),
		})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	s.sessions[id] = held
	for pass, target := range tickets {
		s.tickets[pass] = target
	}
	return &blobv1.PresignUploadResponse{SessionId: id, Files: targets}, nil
}

func (s *Store) prune() {
	now := s.now()
	for id, held := range s.sessions {
		if now.After(held.expiresAt) {
			delete(s.sessions, id)
		}
	}
	for pass, held := range s.tickets {
		if now.After(held.session.expiresAt) {
			delete(s.tickets, pass)
		}
	}
}

func (s *Store) VerifyUploadSignature(_ context.Context, req *blobv1.VerifyUploadSignatureRequest) (*blobv1.VerifyUploadSignatureResponse, error) {
	s.mu.Lock()
	held, ok := s.sessions[req.GetSessionId()]
	s.mu.Unlock()
	if !ok || s.now().After(held.expiresAt) {
		return &blobv1.VerifyUploadSignatureResponse{Valid: false}, nil
	}

	f := req.GetFile()
	expected := sign(held.secret, held.id, completion{
		Key:      f.GetKey(),
		Name:     f.GetName(),
		Size:     f.GetSize(),
		MimeType: f.GetMimeType(),
	})
	if !hmac.Equal([]byte(expected), []byte(req.GetSignature())) {
		return &blobv1.VerifyUploadSignatureResponse{Valid: false}, nil
	}
	return &blobv1.VerifyUploadSignatureResponse{Valid: true, Metadata: held.metadata}, nil
}

func (s *Store) GetUploadStatus(_ context.Context, req *blobv1.GetUploadStatusRequest) (*blobv1.GetUploadStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.sessions[req.GetSessionId()]
	if !ok {
		return &blobv1.GetUploadStatusResponse{State: blobv1.UploadState_UPLOAD_STATE_UNSPECIFIED}, nil
	}
	done := true
	for _, one := range held.files {
		if !one.succeeded {
			done = false
		}
	}
	switch {
	case done && len(held.files) > 0:
		return &blobv1.GetUploadStatusResponse{State: blobv1.UploadState_UPLOAD_STATE_SUCCEEDED}, nil
	case s.now().After(held.expiresAt):
		return &blobv1.GetUploadStatusResponse{State: blobv1.UploadState_UPLOAD_STATE_EXPIRED}, nil
	default:
		return &blobv1.GetUploadStatusResponse{State: blobv1.UploadState_UPLOAD_STATE_PENDING}, nil
	}
}

func (s *Store) pathOf(key string) (string, error) {
	path := filepath.Join(s.dir, filepath.FromSlash(key))
	within := filepath.Clean(s.dir) + string(filepath.Separator)
	if key == "" || !strings.HasPrefix(path, within) {
		return "", fmt.Errorf("upload key %q leaves the store", key)
	}
	return path, nil
}

func (s *Store) handlePut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pass := r.URL.Query().Get("ticket")
	s.mu.Lock()
	held, ok := s.tickets[pass]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "no upload is expected here", http.StatusForbidden)
		return
	}
	if s.now().After(held.session.expiresAt) {
		s.mu.Lock()
		delete(s.tickets, pass)
		s.mu.Unlock()
		http.Error(w, "this upload expired", http.StatusForbidden)
		return
	}

	if err := s.write(held.file.key, http.MaxBytesReader(w, r.Body, held.file.size)); err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			http.Error(w, fmt.Sprintf("this upload was signed for %d bytes", held.file.size), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.deliver(r.Context(), held.session, held.file); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	s.mu.Lock()
	held.file.succeeded = true
	delete(s.tickets, pass)
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *Store) write(key string, body io.Reader) error {
	path, err := s.pathOf(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("make room for %s: %w", key, err)
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".partial-*")
	if err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(out.Name())
	}()
	if _, err := io.Copy(out, body); err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}
	if err := os.Rename(out.Name(), path); err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}
	return nil
}

type completion struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}

type callback struct {
	SessionID string     `json:"sessionId"`
	Signature string     `json:"signature"`
	File      completion `json:"file"`
}

func sign(secret []byte, sessionID string, file completion) string {
	mac := hmac.New(sha256.New, secret)
	payload, _ := json.Marshal(callback{SessionID: sessionID, File: file})
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Store) deliver(ctx context.Context, held *session, one *file) error {
	if held.callbackBaseURL == "" {
		return nil
	}
	done := completion{Key: one.key, Name: one.name, Size: one.size, MimeType: one.mimeType}
	body, err := json.Marshal(callback{SessionID: held.id, Signature: sign(held.secret, held.id, done), File: done})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, held.callbackBaseURL+"?op=callback", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("deliver the upload callback: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("deliver the upload callback: the app answered %d", resp.StatusCode)
	}
	return nil
}
