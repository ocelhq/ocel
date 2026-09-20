package ocel_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	ocel "github.com/ocelhq/ocel/sdk"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	storeToken  = "runtime-token"
	storeBucket = "project-env-avatars"
	publicBase  = "https://cdn.example.com"
)

type storedObject struct {
	data        []byte
	contentType string
	metadata    map[string]string
	etag        string
}

type stagedUpload struct {
	key         string
	contentType string
	metadata    map[string]string
	parts       map[int32][]byte
}

type fakeStore struct {
	mu sync.Mutex

	base    string
	objects map[string]*storedObject
	uploads map[string]*stagedUpload
	version int

	pageSize        int
	externalRefusal string
	refusePart      int32

	signed  []*bucketv1.SignRequest
	created []string
	aborted []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		objects:  map[string]*storedObject{},
		uploads:  map[string]*stagedUpload{},
		pageSize: 1000,
	}
}

func (s *fakeStore) info(key string, held *storedObject) *bucketv1.ObjectInfo {
	return &bucketv1.ObjectInfo{
		Key:         key,
		Size:        int64(len(held.data)),
		Etag:        held.etag,
		ContentType: held.contentType,
		UploadedAt:  timestamppb.New(time.Unix(1700000000, 0).UTC()),
		Metadata:    held.metadata,
	}
}

func (s *fakeStore) put(key string, data []byte, contentType string, metadata map[string]string) *storedObject {
	s.version++
	held := &storedObject{
		data:        slices.Clone(data),
		contentType: contentType,
		metadata:    metadata,
		etag:        fmt.Sprintf("etag-%d", s.version),
	}
	s.objects[key] = held
	return held
}

func (s *fakeStore) Head(_ context.Context, req *bucketv1.HeadRequest) (*bucketv1.HeadResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.objects[req.GetKey()]
	if !ok {
		return &bucketv1.HeadResponse{}, nil
	}
	return &bucketv1.HeadResponse{Object: s.info(req.GetKey(), held)}, nil
}

func (s *fakeStore) List(_ context.Context, req *bucketv1.ListRequest) (*bucketv1.ListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		if strings.HasPrefix(key, req.GetPrefix()) && key > req.GetCursor() {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	size := s.pageSize
	if limit := int(req.GetLimit()); limit > 0 && limit < size {
		size = limit
	}
	cursor := ""
	if len(keys) > size {
		keys = keys[:size]
		cursor = keys[len(keys)-1]
	}
	res := &bucketv1.ListResponse{NextCursor: cursor}
	for _, key := range keys {
		res.Objects = append(res.Objects, s.info(key, s.objects[key]))
	}
	return res, nil
}

func (s *fakeStore) Delete(_ context.Context, req *bucketv1.DeleteRequest) (*bucketv1.DeleteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range req.GetKeys() {
		delete(s.objects, key)
	}
	return &bucketv1.DeleteResponse{}, nil
}

func (s *fakeStore) Copy(_ context.Context, req *bucketv1.CopyRequest) (*bucketv1.CopyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.objects[req.GetSourceKey()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such key"))
	}
	copied := s.put(req.GetDestinationKey(), held.data, held.contentType, held.metadata)
	return &bucketv1.CopyResponse{Object: s.info(req.GetDestinationKey(), copied)}, nil
}

func (s *fakeStore) Sign(_ context.Context, req *bucketv1.SignRequest) (*bucketv1.SignResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signed = append(s.signed, req)
	if req.GetAudience() == bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL && s.externalRefusal != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(s.externalRefusal))
	}
	target := &bucketv1.PresignedTarget{
		Url:     s.base + "/o/" + req.GetKey(),
		Key:     req.GetKey(),
		Headers: map[string]string{"x-signed-by": "fake"},
	}
	switch req.GetOperation() {
	case bucketv1.SignedOperation_SIGNED_OPERATION_PUT:
		target.Method = http.MethodPut
		for name, value := range req.GetConstraints().GetMetadata() {
			target.Headers["x-amz-meta-"+name] = value
		}
	case bucketv1.SignedOperation_SIGNED_OPERATION_POST_UPLOAD:
		target.Method = http.MethodPost
		target.Fields = map[string]string{"key": req.GetKey(), "policy": "signed"}
	default:
		target.Method = http.MethodGet
	}
	return &bucketv1.SignResponse{Target: target}, nil
}

func (s *fakeStore) CreateMultipart(_ context.Context, req *bucketv1.CreateMultipartRequest) (*bucketv1.CreateMultipartResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("upload-%d", len(s.uploads)+1)
	s.uploads[id] = &stagedUpload{
		key:         req.GetKey(),
		contentType: req.GetContentType(),
		metadata:    req.GetMetadata(),
		parts:       map[int32][]byte{},
	}
	s.created = append(s.created, id)
	return &bucketv1.CreateMultipartResponse{UploadId: id}, nil
}

func (s *fakeStore) SignParts(_ context.Context, req *bucketv1.SignPartsRequest) (*bucketv1.SignPartsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.uploads[req.GetUploadId()]; !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such upload"))
	}
	res := &bucketv1.SignPartsResponse{}
	for _, number := range req.GetPartNumbers() {
		res.Parts = append(res.Parts, &bucketv1.SignedPart{
			PartNumber: number,
			Url:        fmt.Sprintf("%s/p/%s/%d", s.base, req.GetUploadId(), number),
			Headers:    map[string]string{"x-signed-by": "fake"},
		})
	}
	return res, nil
}

func (s *fakeStore) CompleteMultipart(_ context.Context, req *bucketv1.CompleteMultipartRequest) (*bucketv1.CompleteMultipartResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	staged, ok := s.uploads[req.GetUploadId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such upload"))
	}
	held, exists := s.objects[staged.key]
	if req.GetIfNoneMatch() == "*" && exists {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the key is already taken"))
	}
	if tag := req.GetIfMatch(); tag != "" && (!exists || held.etag != tag) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the key does not carry that version"))
	}
	numbers := make([]int32, 0, len(req.GetParts()))
	for _, part := range req.GetParts() {
		numbers = append(numbers, part.GetPartNumber())
	}
	slices.Sort(numbers)
	var joined []byte
	for _, number := range numbers {
		joined = append(joined, staged.parts[number]...)
	}
	delete(s.uploads, req.GetUploadId())
	stored := s.put(staged.key, joined, staged.contentType, staged.metadata)
	return &bucketv1.CompleteMultipartResponse{Object: s.info(staged.key, stored)}, nil
}

func (s *fakeStore) AbortMultipart(_ context.Context, req *bucketv1.AbortMultipartRequest) (*bucketv1.AbortMultipartResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aborted = append(s.aborted, req.GetUploadId())
	delete(s.uploads, req.GetUploadId())
	return &bucketv1.AbortMultipartResponse{}, nil
}

func (s *fakeStore) PresignUpload(context.Context, *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("the go sdk drives no browser upload"))
}

func (s *fakeStore) VerifyUploadSignature(context.Context, *bucketv1.VerifyUploadSignatureRequest) (*bucketv1.VerifyUploadSignatureResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("the go sdk drives no browser upload"))
}

func (s *fakeStore) GetUploadStatus(context.Context, *bucketv1.GetUploadStatusRequest) (*bucketv1.GetUploadStatusResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("the go sdk drives no browser upload"))
}

func (s *fakeStore) CompleteUpload(context.Context, *bucketv1.CompleteUploadRequest) (*bucketv1.CompleteUploadResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("the go sdk drives no browser upload"))
}

func (s *fakeStore) object(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/o/")
	s.mu.Lock()
	defer s.mu.Unlock()
	held, exists := s.objects[key]

	switch r.Method {
	case http.MethodGet:
		if !exists {
			http.Error(w, "no such key", http.StatusNotFound)
			return
		}
		data := held.data
		status := http.StatusOK
		if span := r.Header.Get("Range"); span != "" {
			from, to, ok := byteSpan(span, len(data))
			if !ok {
				http.Error(w, "unsatisfiable range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			data = data[from:to]
			status = http.StatusPartialContent
		}
		w.Header().Set("Content-Type", held.contentType)
		w.WriteHeader(status)
		_, _ = w.Write(data)
	case http.MethodPut:
		if r.Header.Get("If-None-Match") == "*" && exists {
			http.Error(w, "the key is already taken", http.StatusPreconditionFailed)
			return
		}
		if tag := r.Header.Get("If-Match"); tag != "" && (!exists || held.etag != tag) {
			http.Error(w, "the key does not carry that version", http.StatusPreconditionFailed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		metadata := map[string]string{}
		for name, values := range r.Header {
			lowered := strings.ToLower(name)
			if after, found := strings.CutPrefix(lowered, "x-amz-meta-"); found {
				metadata[after] = values[0]
			}
		}
		stored := s.put(key, body, r.Header.Get("Content-Type"), metadata)
		w.Header().Set("ETag", stored.etag)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func (s *fakeStore) part(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/p/")
	id, number, found := strings.Cut(rest, "/")
	if !found {
		http.Error(w, "malformed part path", http.StatusBadRequest)
		return
	}
	parsed, err := strconv.Atoi(number)
	if err != nil {
		http.Error(w, "malformed part number", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refusePart != 0 && int32(parsed) == s.refusePart {
		http.Error(w, "the store refused this part", http.StatusInsufficientStorage)
		return
	}
	staged, ok := s.uploads[id]
	if !ok {
		http.Error(w, "no such upload", http.StatusNotFound)
		return
	}
	staged.parts[int32(parsed)] = body
	w.Header().Set("ETag", fmt.Sprintf("part-%s-%d", id, parsed))
	w.WriteHeader(http.StatusOK)
}

func byteSpan(header string, length int) (from, to int, ok bool) {
	span, found := strings.CutPrefix(header, "bytes=")
	if !found {
		return 0, 0, false
	}
	first, last, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, false
	}
	from, err := strconv.Atoi(first)
	if err != nil || from >= length {
		return 0, 0, false
	}
	to = length
	if last != "" {
		parsed, err := strconv.Atoi(last)
		if err != nil {
			return 0, 0, false
		}
		to = min(parsed+1, length)
	}
	if to <= from {
		return 0, 0, false
	}
	return from, to, true
}

func serveStore(t *testing.T, store *fakeStore) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := bucketv1connect.NewBucketServiceHandler(store)
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !channel.VerifyAuthHeader(r.Header.Get("Authorization"), storeToken) {
			http.Error(w, "this request carries no valid session token", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/o/", store.object)
	mux.HandleFunc("/p/", store.part)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	store.base = srv.URL
	return srv
}

func bucketFixture(t *testing.T, store *fakeStore) *ocel.BucketStore {
	t.Helper()
	return declaredBucket(t, store, fmt.Sprintf(`,"publicBaseUrl":%q`, publicBase))
}

func privateBucketFixture(t *testing.T, store *fakeStore) *ocel.BucketStore {
	t.Helper()
	return declaredBucket(t, store, "")
}

func declaredBucket(t *testing.T, store *fakeStore, public string) *ocel.BucketStore {
	t.Helper()
	srv := serveStore(t, store)
	t.Setenv(constants.RuntimeAddressEnvName, srv.URL)
	t.Setenv(channel.SessionTokenEnvVar, storeToken)
	t.Setenv("OCEL_RESOURCE_BUCKET_avatars", fmt.Sprintf(
		`{"name":"avatars","bucket":{"bucket":%q%s}}`, storeBucket, public,
	))
	return ocel.Bucket("avatars")
}
