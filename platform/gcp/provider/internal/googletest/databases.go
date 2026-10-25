package googletest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	CreateDenied  bool

	Layouts                         []string
	LayoutUnserved                  int
	LayoutContended                 int
	OperationPolls                  int
	LaidOutAfterAnOperationFinished []string

	operations int
	polled     map[string]int
	finished   bool
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
	database, layout, laidOut := strings.Cut(id, "/collectionGroups/")
	polling := strings.Contains(id, "/operations/")
	switch {
	case nested && polling && r.Method == http.MethodGet:
		s.pollOperation(w, r.URL.Path)
	case nested && laidOut:
		if s.Held[database] == nil || s.LayoutUnserved > 0 {
			s.LayoutUnserved = max(s.LayoutUnserved-1, 0)
			WriteNotFound(w)
			return
		}
		if s.LayoutContended > 0 {
			s.LayoutContended--
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":409,"status":"ABORTED","message":"Please retry, underlying data changed."}}`))
			return
		}
		s.Layouts = append(s.Layouts, r.Method+" "+layout)
		if s.finished {
			s.LaidOutAfterAnOperationFinished = append(s.LaidOutAfterAnOperationFinished, r.Method+" "+layout)
		}
		s.startOperation(w, database)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/"+Project+"/databases":
		asked := ReadBody[firestoreadmin.GoogleFirestoreAdminV1Database](w, r)
		if asked == nil {
			return
		}
		id := r.URL.Query().Get("databaseId")
		if s.CreateDenied {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"The caller does not have permission"}}`))
			return
		}
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

func (s *Databases) startOperation(w http.ResponseWriter, database string) {
	if s.OperationPolls == 0 {
		writeDoneDatabaseOperation(w)
		return
	}
	s.operations++
	WriteBody(w, &firestoreadmin.GoogleLongrunningOperation{Name: "projects/" + Project + "/databases/" + database + "/operations/" + strconv.Itoa(s.operations)})
}

func (s *Databases) pollOperation(w http.ResponseWriter, path string) {
	if s.polled == nil {
		s.polled = map[string]int{}
	}
	s.polled[path]++
	done := s.polled[path] >= s.OperationPolls
	s.finished = s.finished || done
	WriteBody(w, &firestoreadmin.GoogleLongrunningOperation{Name: strings.TrimPrefix(path, "/v1/"), Done: done})
}

func writeDoneDatabaseOperation(w http.ResponseWriter) {
	if err := json.NewEncoder(w).Encode(&firestoreadmin.GoogleLongrunningOperation{Name: "operations/done", Done: true}); err != nil {
		panic(err)
	}
}
