package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestTheFakeRegistryListsNoTagItAnswersAsNotFound(t *testing.T) {
	gone := shopWebPackage + "/tags/sha256-gone"
	server := &googletest.Run{
		Tagged:  map[string][]string{shopWebPackage: {"sha256-kept", "sha256-gone"}},
		Missing: []string{gone},
	}
	serve := server.Serve(t)

	read := httptest.NewRecorder()
	serve(read, httptest.NewRequest(http.MethodGet, "/v1/"+gone, nil))
	if read.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d, want %d for a tag a release pruned", gone, read.Code, http.StatusNotFound)
	}

	list := httptest.NewRecorder()
	serve(list, httptest.NewRequest(http.MethodGet, "/v1/"+shopWebPackage+"/tags", nil))
	var listed struct {
		Tags []struct{ Name string } `json:"tags"`
	}
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tag := range listed.Tags {
		names = append(names, tag.Name)
	}
	if want := []string{shopWebPackage + "/tags/sha256-kept"}; !slices.Equal(names, want) {
		t.Errorf("the tag listing = %v, want %v: a tag its GET answers 404 for is not listed either", names, want)
	}
}
