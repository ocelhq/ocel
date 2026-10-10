package gcp

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	raw "google.golang.org/api/storage/v1"
)

type hmacServer struct {
	mu      sync.Mutex
	keys    map[string]*raw.HmacKeyMetadata
	created int
	deleted []string
}

func (s *hmacServer) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = map[string]*raw.HmacKeyMetadata{}
	}
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/storage/v1/projects/acme-prod/hmacKeys")
	id := strings.TrimPrefix(path, "/")
	switch {
	case path == "" && r.Method == http.MethodPost:
		s.created++
		key := &raw.HmacKeyMetadata{
			AccessId:            "GOOG1E" + strconv.Itoa(s.created),
			ServiceAccountEmail: r.URL.Query().Get("serviceAccountEmail"),
			State:               "ACTIVE",
			ProjectId:           "acme-prod",
			TimeCreated:         "2026-10-10T00:00:00Z",
			Updated:             "2026-10-10T00:00:00Z",
		}
		s.keys[key.AccessId] = key
		writeBody(w, &raw.HmacKey{Metadata: key, Secret: "secret-" + key.AccessId})
	case path == "" && r.Method == http.MethodGet:
		var listed []*raw.HmacKeyMetadata
		for _, key := range s.keys {
			if key.ServiceAccountEmail == r.URL.Query().Get("serviceAccountEmail") {
				listed = append(listed, key)
			}
		}
		slices.SortFunc(listed, func(a, b *raw.HmacKeyMetadata) int { return strings.Compare(a.AccessId, b.AccessId) })
		writeBody(w, &raw.HmacKeysMetadata{Items: listed})
	case r.Method == http.MethodGet:
		s.withKey(w, id, func(key *raw.HmacKeyMetadata) { writeBody(w, key) })
	case r.Method == http.MethodPut:
		update := readBody[raw.HmacKeyMetadata](w, r)
		if update == nil {
			return
		}
		s.withKey(w, id, func(key *raw.HmacKeyMetadata) {
			key.State = update.State
			writeBody(w, key)
		})
	case r.Method == http.MethodDelete:
		s.withKey(w, id, func(key *raw.HmacKeyMetadata) {
			if key.State == "ACTIVE" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"code":400,"message":"Cannot delete an active key."}}`))
				return
			}
			delete(s.keys, id)
			s.deleted = append(s.deleted, id)
			w.WriteHeader(http.StatusNoContent)
		})
	default:
		t.Errorf("the HMAC server was asked %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *hmacServer) withKey(w http.ResponseWriter, id string, use func(*raw.HmacKeyMetadata)) {
	key, found := s.keys[id]
	if !found {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"HMAC key not found"}}`))
		return
	}
	use(key)
}

func (s *hmacServer) active() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, key := range s.keys {
		if key.State == "ACTIVE" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}
