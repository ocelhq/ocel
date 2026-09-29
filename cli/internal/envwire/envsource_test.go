package envwire

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/envsource"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func infisicalConfig() *projectconfig.Config {
	tiers := envsource.DefaultTiers()
	tiers.Production = envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
		Project: "p-1", Environment: "prod",
		Auth: envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVariable: "INFISICAL_CLIENT_ID", ClientSecretVariable: "INFISICAL_CLIENT_SECRET"},
	}}
	return &projectconfig.Config{EnvSource: tiers}
}

func TestScopeNamesTheEnvSourceATierReadsAndTheCredentialsItLogsInWith(t *testing.T) {
	cfg := infisicalConfig()

	want := variables.EnvSource{ID: "infisical:p-1/prod", Credentials: []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"}}
	if got := Scope(cfg, false, "").EnvSource; !reflect.DeepEqual(got, want) {
		t.Errorf("production EnvSource = %+v, want %+v", got, want)
	}
	if got := Scope(cfg, true, "pr-12").EnvSource; !reflect.DeepEqual(got, variables.EnvSource{ID: "builtin"}) {
		t.Errorf("preview EnvSource = %+v, want builtin with no credentials", got)
	}
	if got := DevScope(cfg).EnvSource; !reflect.DeepEqual(got, variables.EnvSource{}) {
		t.Errorf("dev EnvSource = %+v, want none: ocel dev reads on this machine", got)
	}
}

func TestASyncedEnvSourceIsWhatTheProviderSaysItRead(t *testing.T) {
	synced := EnvSourceOf(&envvarsv1.SyncEnvSourceResponse{
		Status: &envvarsv1.EnvSourceStatus{
			EnvSource:   "infisical:p-1/prod",
			CanCreate:   true,
			CanUpdate:   true,
			Links:       []*envvarsv1.FolderLink{{Folder: "", Url: "https://infisical.example/root"}},
			Credentials: []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"},
		},
		Present: []*envvarsv1.Cell{{Key: "STRIPE_KEY"}, {Folder: "/web", Key: "API_URL"}},
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
		t.Errorf("EnvSourceOf = %+v, want %+v", synced, want)
	}
}

func TestAnEnvSourceIsSyncedForTheRootAndEveryAppFolder(t *testing.T) {
	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "web", Folder: "/web"}, {Name: "api", Folder: "/api"}, {Name: "admin", Folder: "/web"}}}
	if got, want := Folders(cfg), []string{"", "/api", "/web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Folders = %q, want %q", got, want)
	}
}
