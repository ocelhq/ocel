package googletest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/logging/v2"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

type LogViews struct {
	Mu      sync.Mutex
	Views   map[string]*logging.LogView
	Created []string
	Patched []string
	Deleted []string
}

func NewLogViews() *LogViews { return &LogViews{Views: map[string]*logging.LogView{}} }

func (v *LogViews) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.Mu.Lock()
	defer v.Mu.Unlock()
	name := strings.TrimPrefix(r.URL.Path, "/v2/")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(name, "/views"):
		asked := ReadBody[logging.LogView](w, r)
		if asked == nil {
			return
		}
		asked.Name = name + "/" + r.URL.Query().Get("viewId")
		if v.Views[asked.Name] != nil {
			w.WriteHeader(http.StatusConflict)
			WriteBody(w, map[string]any{"error": map[string]any{"code": http.StatusConflict, "message": "already exists"}})
			return
		}
		v.Views[asked.Name] = asked
		v.Created = append(v.Created, asked.Name)
		WriteBody(w, asked)
	case v.Views[name] == nil:
		WriteNotFound(w)
	case r.Method == http.MethodGet:
		WriteBody(w, v.Views[name])
	case r.Method == http.MethodPatch:
		asked := ReadBody[logging.LogView](w, r)
		if asked == nil {
			return
		}
		if r.URL.Query().Get("updateMask") == "filter" {
			v.Views[name].Filter = asked.Filter
		}
		v.Patched = append(v.Patched, name)
		WriteBody(w, v.Views[name])
	case r.Method == http.MethodDelete:
		delete(v.Views, name)
		v.Deleted = append(v.Deleted, name)
		WriteBody(w, map[string]any{})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (v *LogViews) Open(t *testing.T) *gcloud.Clients {
	t.Helper()
	server := httptest.NewServer(v)
	t.Cleanup(server.Close)
	return Clients(server.URL)
}
