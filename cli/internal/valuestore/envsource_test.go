package valuestore

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/envsource"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
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

func TestAnEnvSourceIsSentAsItsKindAndOptionsWithWhatWasReadInFolderThenKeyOrder(t *testing.T) {
	options := []byte(`{"command":["vault"],"format":"json"}`)
	descriptor, err := envsource.NewDescriptor("exec", options)
	if err != nil {
		t.Fatal(err)
	}
	sent := envSourceMessage(descriptor, map[variablestore.Cell]envsource.Value{
		{Folder: "/web", Key: "A"}: {Plaintext: []byte("3")},
		{Key: "B"}:                 {Plaintext: []byte("2"), Version: "s1@9"},
		{Key: "A"}:                 {Plaintext: []byte("1")},
	})

	if sent.GetKind() != "exec" || string(sent.GetOptions()) != string(options) {
		t.Errorf("sent %s %s, want exec with its options as configured", sent.GetKind(), sent.GetOptions())
	}
	var got []string
	for _, value := range sent.GetValues() {
		got = append(got, value.GetCell().GetFolder()+" "+value.GetCell().GetKey()+"="+value.GetValue())
	}
	if want := []string{" A=1", " B=2", "/web A=3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("values = %q, want %q", got, want)
	}
}
