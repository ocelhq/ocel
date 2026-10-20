package googletest

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

type TierAdmissions struct {
	Mu       sync.Mutex
	Withheld int
	Asked    []string
}

func (a *TierAdmissions) Serve(w http.ResponseWriter, r *http.Request) bool {
	storage := r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/iam/testPermissions")
	asking := r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":testIamPermissions")
	if !storage && !asking {
		return false
	}
	var asked struct {
		Permissions []string `json:"permissions"`
	}
	if storage {
		asked.Permissions = r.URL.Query()["permissions"]
	} else if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return true
	}
	a.Mu.Lock()
	a.Asked = append(a.Asked, r.URL.Path)
	granted := asked.Permissions
	if a.Withheld > 0 {
		a.Withheld--
		granted = nil
	}
	a.Mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if storage {
		WriteBody(w, map[string]any{"kind": "storage#testIamPermissionsResponse", "permissions": granted})
		return true
	}
	WriteBody(w, map[string]any{"permissions": granted})
	return true
}
