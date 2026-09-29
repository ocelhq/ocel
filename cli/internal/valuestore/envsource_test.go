package valuestore

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func TestASyncedEnvSourceIsWhatTheProviderSaysItRead(t *testing.T) {
	synced := envSourceOf(&variablestorev1.SyncEnvSourceResponse{
		Status: &variablestorev1.EnvSourceStatus{
			EnvSource:   "infisical:p-1/prod",
			CanCreate:   true,
			CanUpdate:   true,
			Links:       []*variablestorev1.FolderLink{{Folder: "", Url: "https://infisical.example/root"}},
			Credentials: []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"},
		},
		Present: []*variablestorev1.Cell{{Key: "STRIPE_KEY"}, {Folder: "/web", Key: "API_URL"}},
	})

	want := variables.EnvSource{
		ID:          "infisical:p-1/prod",
		CanCreate:   true,
		CanUpdate:   true,
		URLs:        map[string]string{"": "https://infisical.example/root"},
		Present:     []variables.Cell{{Key: "STRIPE_KEY"}, {Folder: "/web", Key: "API_URL"}},
		Credentials: []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"},
	}
	if !reflect.DeepEqual(synced, want) {
		t.Errorf("envSourceOf = %+v, want %+v", synced, want)
	}
}

func TestAnEnvSourceIsSyncedForTheRootAndEveryAppFolder(t *testing.T) {
	cfg := &project.Project{Apps: []project.App{{Name: "web", Folder: "/web"}, {Name: "api", Folder: "/api"}, {Name: "admin", Folder: "/web"}}}
	if got, want := syncedFolders(cfg), []string{"", "/api", "/web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("syncedFolders = %q, want %q", got, want)
	}
}
