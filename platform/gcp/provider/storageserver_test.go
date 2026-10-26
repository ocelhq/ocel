package gcp

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	raw "google.golang.org/api/storage/v1"
)

type storageServer struct {
	mu       sync.Mutex
	buckets  map[string]*raw.Bucket
	policies map[string]*raw.Policy
	objects  map[string][]string
	created  []*raw.Bucket
	patched  []*raw.Bucket
	cleared  []string
	setters  []*raw.Policy
	deleted  []string
	removed  []string
	writes   []string

	listed        int
	objectsListed int

	takenElsewhere  bool
	forbiddingReads bool
	staleSets       int
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
		if r.Method != http.MethodGet {
			s.writes = append(s.writes, r.Method+" "+r.URL.Path)
		}
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/storage/v1/")
		parts := strings.Split(path, "/")
		switch {
		case r.Method == http.MethodPost && path == "b":
			s.create(w, r)
		case r.Method == http.MethodGet && path == "b":
			s.listed++
			s.listBuckets(w, r.URL.Query().Get("prefix"))
		case len(parts) == 2 && parts[0] == "b":
			s.bucket(w, r, parts[1])
		case len(parts) == 3 && parts[2] == "iam":
			s.policy(w, r, parts[1])
		case len(parts) == 3 && parts[2] == "o":
			s.objectsListed++
			s.list(w, parts[1], r.URL.Query().Get("matchGlob"))
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
	bucket := googletest.ReadBody[raw.Bucket](w, r)
	if bucket == nil {
		return
	}
	if _, held := s.buckets[bucket.Name]; s.takenElsewhere || held {
		googletest.Conflicted(w, "Your previous request to create the named bucket succeeded and you already own it.")
		return
	}
	s.created = append(s.created, bucket)
	stored := *bucket
	if stored.StorageClass == "" {
		stored.StorageClass = "STANDARD"
	}
	if stored.SoftDeletePolicy == nil {
		stored.SoftDeletePolicy = &raw.BucketSoftDeletePolicy{RetentionDurationSeconds: 7 * 24 * 60 * 60}
	}
	effective(stored.RetentionPolicy)
	s.buckets[bucket.Name] = &stored
	googletest.WriteBody(w, &stored)
}

func (s *storageServer) bucket(w http.ResponseWriter, r *http.Request, name string) {
	current, found := s.buckets[name]
	if !found && s.takenElsewhere {
		forbidden(w)
		return
	}
	if !found {
		missingObject(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if s.forbiddingReads {
			forbidden(w)
			return
		}
		googletest.WriteBody(w, current)
	case http.MethodPatch:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var patch raw.Bucket
		var fields map[string]any
		if json.Unmarshal(body, &patch) != nil || json.Unmarshal(body, &fields) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.patched = append(s.patched, &patch)
		cleared := nullFields("", fields)
		s.cleared = append(s.cleared, cleared...)
		s.applyPatch(current, &patch, cleared)
		googletest.WriteBody(w, current)
	case http.MethodDelete:
		if len(s.objects[name]) > 0 {
			googletest.Conflicted(w, "The bucket you tried to delete is not empty.")
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
		googletest.WriteBody(w, current)
	case http.MethodPut:
		asked := googletest.ReadBody[raw.Policy](w, r)
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
		googletest.WriteBody(w, asked)
	}
}

func (s *storageServer) list(w http.ResponseWriter, bucket, glob string) {
	if _, found := s.buckets[bucket]; !found {
		missingObject(w)
		return
	}
	listed := &raw.Objects{Kind: "storage#objects"}
	for _, name := range s.objects[bucket] {
		if glob != "" && !matchesGlob(glob, name) {
			continue
		}
		listed.Items = append(listed.Items, &raw.Object{Name: name, Bucket: bucket})
	}
	googletest.WriteBody(w, listed)
}

func matchesGlob(glob, name string) bool {
	head, alternatives, braced := strings.Cut(glob, "{")
	if !braced {
		matched, _ := path.Match(glob, name)
		return matched
	}
	alternatives, tail, _ := strings.Cut(alternatives, "}")
	for _, alternative := range strings.Split(alternatives, ",") {
		if matchesGlob(head+alternative+tail, name) {
			return true
		}
	}
	return false
}

func (s *storageServer) listBuckets(w http.ResponseWriter, prefix string) {
	listed := &raw.Buckets{Kind: "storage#buckets"}
	for _, name := range slices.Sorted(maps.Keys(s.buckets)) {
		if strings.HasPrefix(name, prefix) {
			listed.Items = append(listed.Items, s.buckets[name])
		}
	}
	googletest.WriteBody(w, listed)
}

func missingObject(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error":{"code":404,"message":"The specified bucket does not exist."}}`))
}

func forbidden(w http.ResponseWriter) {
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"error":{"code":403,"message":"The caller does not have storage.buckets.get access to the Google Cloud Storage bucket."}}`))
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

func (s *storageServer) listings() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listed
}

func (s *storageServer) objectListings() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objectsListed
}

func (s *storageServer) granted(bucket string) *raw.Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policies[bucket]
}

func (s *storageServer) wrote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

func (s *storageServer) drift(bucket string, change func(*raw.Bucket)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s.buckets[bucket])
}

func nullFields(prefix string, fields map[string]any) []string {
	var null []string
	for key, value := range fields {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch nested := value.(type) {
		case nil:
			null = append(null, path)
		case map[string]any:
			null = append(null, nullFields(path, nested)...)
		}
	}
	return null
}

func (s *storageServer) applyPatch(current, patch *raw.Bucket, cleared []string) {
	if patch.IamConfiguration != nil {
		current.IamConfiguration = patch.IamConfiguration
	}
	if patch.Cors != nil {
		current.Cors = patch.Cors
	}
	if patch.Lifecycle != nil {
		current.Lifecycle = patch.Lifecycle
	}
	if patch.StorageClass != "" {
		current.StorageClass = patch.StorageClass
	}
	if patch.Versioning != nil {
		current.Versioning = patch.Versioning
	}
	if patch.SoftDeletePolicy != nil {
		current.SoftDeletePolicy = patch.SoftDeletePolicy
	}
	if patch.RetentionPolicy != nil {
		current.RetentionPolicy = patch.RetentionPolicy
		effective(current.RetentionPolicy)
	}
	if patch.Encryption != nil && patch.Encryption.DefaultKmsKeyName != "" {
		current.Encryption = patch.Encryption
	}
	if patch.Logging != nil {
		current.Logging = patch.Logging
	}
	for key, value := range patch.Labels {
		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		current.Labels[key] = value
	}
	for _, path := range cleared {
		switch {
		case path == "retentionPolicy":
			current.RetentionPolicy = nil
		case path == "logging":
			current.Logging = nil
		case path == "encryption.defaultKmsKeyName":
			current.Encryption = nil
		case strings.HasPrefix(path, "labels."):
			delete(current.Labels, strings.TrimPrefix(path, "labels."))
		}
	}
}

func effective(policy *raw.BucketRetentionPolicy) {
	if policy != nil && policy.EffectiveTime == "" {
		policy.EffectiveTime = "2026-01-01T00:00:00Z"
	}
}
