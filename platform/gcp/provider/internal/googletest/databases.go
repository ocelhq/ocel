package googletest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	firestoreadmin "google.golang.org/api/firestore/v1"
)

type Databases struct {
	Mu            sync.Mutex
	URL           string
	Held          map[string]*firestoreadmin.GoogleFirestoreAdminV1Database
	InlineTags    map[string]map[string]string
	Deleted       []string
	DeletedTagged []string
	TierTags      *TierTags
}

func DatabaseTagParent(id string) string {
	return "//firestore.googleapis.com/projects/" + Project + "/databases/" + id
}

func NewDatabases(t *testing.T, tags *TierTags) *Databases {
	t.Helper()
	fake := &Databases{Held: map[string]*firestoreadmin.GoogleFirestoreAdminV1Database{}, InlineTags: map[string]map[string]string{}, TierTags: tags}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fake.Serve(t, w, r) }))
	t.Cleanup(server.Close)
	fake.URL = server.URL
	return fake
}

func (s *Databases) Serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	s.Mu.Lock()
	defer s.Mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	id, nested := strings.CutPrefix(r.URL.Path, "/v1/projects/"+Project+"/databases/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/"+Project+"/databases":
		asked := ReadBody[firestoreadmin.GoogleFirestoreAdminV1Database](w, r)
		if asked == nil {
			return
		}
		id := r.URL.Query().Get("databaseId")
		if s.Held[id] != nil {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":409,"status":"ALREADY_EXISTS","message":"database already exists"}}`))
			return
		}
		asked.Name = "projects/" + Project + "/databases/" + id
		s.Held[id] = asked
		if len(asked.Tags) > 0 {
			s.InlineTags[id] = asked.Tags
			if s.TierTags != nil {
				s.TierTags.BindInline(DatabaseTagParent(id), asked.Tags)
			}
		}
		writeDoneDatabaseOperation(w)
	case nested && r.Method == http.MethodGet:
		held := s.Held[id]
		if held == nil {
			WriteNotFound(w)
			return
		}
		WriteBody(w, held)
	case nested && r.Method == http.MethodPatch:
		held := s.Held[id]
		asked := ReadBody[firestoreadmin.GoogleFirestoreAdminV1Database](w, r)
		if held == nil || asked == nil {
			WriteNotFound(w)
			return
		}
		held.DeleteProtectionState = asked.DeleteProtectionState
		writeDoneDatabaseOperation(w)
	case nested && r.Method == http.MethodDelete:
		if s.Held[id] == nil {
			WriteNotFound(w)
			return
		}
		if s.TierTags != nil && len(s.TierTags.Bound(DatabaseTagParent(id))) > 0 {
			s.DeletedTagged = append(s.DeletedTagged, id)
		}
		delete(s.Held, id)
		s.Deleted = append(s.Deleted, id)
		writeDoneDatabaseOperation(w)
	default:
		t.Errorf("Firestore admin was called %s %s, which nothing here serves", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func writeDoneDatabaseOperation(w http.ResponseWriter) {
	if err := json.NewEncoder(w).Encode(&firestoreadmin.GoogleLongrunningOperation{Name: "operations/done", Done: true}); err != nil {
		panic(err)
	}
}
