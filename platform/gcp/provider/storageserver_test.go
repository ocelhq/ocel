package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	raw "google.golang.org/api/storage/v1"
)

type storageServer struct {
	mu       sync.Mutex
	buckets  map[string]*raw.Bucket
	policies map[string]*raw.Policy
	objects  map[string][]string
	created  []*raw.Bucket
	patched  []*raw.Bucket
	setters  []*raw.Policy
	deleted  []string
	removed  []string

	takenElsewhere bool
	staleSets      int
}

func servingStorage(t *testing.T) (*Provider, *storageServer) {
	t.Helper()
	server := &storageServer{buckets: map[string]*raw.Bucket{}, policies: map[string]*raw.Policy{}, objects: map[string][]string{}}
	served := httptest.NewServer(server.serve())
	t.Cleanup(served.Close)
	return pushing(t, served.URL), server
}

func (s *storageServer) serve() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/storage/v1/")
		parts := strings.Split(path, "/")
		switch {
		case r.Method == http.MethodPost && path == "b":
			s.create(w, r)
		case len(parts) == 2 && parts[0] == "b":
			s.bucket(w, r, parts[1])
		case len(parts) == 3 && parts[2] == "iam":
			s.policy(w, r, parts[1])
		case len(parts) == 3 && parts[2] == "o":
			s.list(w, parts[1])
		case len(parts) == 4 && parts[2] == "o" && r.Method == http.MethodDelete:
			name, _ := url.PathUnescape(parts[3])
			s.removed = append(s.removed, name)
			s.objects[parts[1]] = slices.DeleteFunc(s.objects[parts[1]], func(object string) bool { return object == name })
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	}
}

func (s *storageServer) create(w http.ResponseWriter, r *http.Request) {
	bucket := readBody[raw.Bucket](w, r)
	if bucket == nil {
		return
	}
	if s.takenElsewhere {
		conflicted(w, "Your previous request to create the named bucket succeeded and you already own it.")
		return
	}
	s.created = append(s.created, bucket)
	s.buckets[bucket.Name] = bucket
	writeBody(w, bucket)
}

func (s *storageServer) bucket(w http.ResponseWriter, r *http.Request, name string) {
	current, found := s.buckets[name]
	if !found {
		missingObject(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeBody(w, current)
	case http.MethodPatch:
		patch := readBody[raw.Bucket](w, r)
		if patch == nil {
			return
		}
		s.patched = append(s.patched, patch)
		if patch.IamConfiguration != nil {
			current.IamConfiguration = patch.IamConfiguration
		}
		if patch.Cors != nil {
			current.Cors = patch.Cors
		}
		if patch.Lifecycle != nil {
			current.Lifecycle = patch.Lifecycle
		}
		writeBody(w, current)
	case http.MethodDelete:
		if len(s.objects[name]) > 0 {
			conflicted(w, "The bucket you tried to delete is not empty.")
			return
		}
		s.deleted = append(s.deleted, name)
		delete(s.buckets, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *storageServer) policy(w http.ResponseWriter, r *http.Request, name string) {
	if _, found := s.buckets[name]; !found {
		missingObject(w)
		return
	}
	current := s.policies[name]
	if current == nil {
		current = &raw.Policy{Etag: "CAE=", Version: 1}
		s.policies[name] = current
	}
	switch r.Method {
	case http.MethodGet:
		writeBody(w, current)
	case http.MethodPut:
		asked := readBody[raw.Policy](w, r)
		if asked == nil {
			return
		}
		if s.staleSets > 0 {
			s.staleSets--
			w.WriteHeader(http.StatusPreconditionFailed)
			w.Write([]byte(`{"error":{"code":412,"message":"etag mismatch"}}`))
			return
		}
		s.setters = append(s.setters, asked)
		asked.Etag = "CAI" + strconv.Itoa(len(s.setters))
		s.policies[name] = asked
		writeBody(w, asked)
	}
}

func (s *storageServer) list(w http.ResponseWriter, bucket string) {
	if _, found := s.buckets[bucket]; !found {
		missingObject(w)
		return
	}
	listed := &raw.Objects{Kind: "storage#objects"}
	for _, name := range s.objects[bucket] {
		listed.Items = append(listed.Items, &raw.Object{Name: name, Bucket: bucket})
	}
	writeBody(w, listed)
}

func missingObject(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error":{"code":404,"message":"The specified bucket does not exist."}}`))
}

func (s *storageServer) holding(bucket *raw.Bucket, objects ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, _ := json.Marshal(bucket)
	var kept raw.Bucket
	_ = json.Unmarshal(encoded, &kept)
	s.buckets[bucket.Name] = &kept
	s.objects[bucket.Name] = objects
}

func (s *storageServer) granted(bucket string) *raw.Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policies[bucket]
}
