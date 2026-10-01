package variablestoreserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envsource"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

type infisical struct {
	mu       sync.Mutex
	secrets  map[string]map[string]string
	versions map[string]int
	created  []string
	updated  []string
	loggedIn []string
	approval bool
}

func newInfisical(t *testing.T) (*infisical, *httptest.Server) {
	t.Helper()
	fake := &infisical{secrets: map[string]map[string]string{"/": {}, "/web": {}}, versions: map[string]int{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return fake, server
}

func (f *infisical) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v1/auth/"):
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !loggedIn(strings.TrimPrefix(r.URL.Path, "/api/v1/auth/"), body) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Invalid credentials"})
			return
		}
		f.loggedIn = append(f.loggedIn, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expiresIn": 3600})
	case r.Header.Get("Authorization") != "Bearer token":
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/api/v1/projects/p-1":
		_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"orgId": "org-1"}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v4/secrets":
		folder, found := f.secrets[r.URL.Query().Get("secretPath")]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		secrets := []map[string]any{}
		for key, value := range folder {
			secrets = append(secrets, map[string]any{"id": key, "secretKey": key, "secretValue": value, "version": f.version(r.URL.Query().Get("secretPath"), key)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": secrets})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v4/secrets/"):
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		key := strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/")
		if _, exists := f.secrets[body["secretPath"]][key]; exists {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Secret '" + key + "' already exists"})
			return
		}
		f.secrets[body["secretPath"]][key] = body["secretValue"]
		f.created = append(f.created, body["secretPath"]+" "+key+" "+body["secretComment"])
		_ = json.NewEncoder(w).Encode(map[string]any{"secret": map[string]any{"id": key}})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v4/secrets/"):
		path, key := r.URL.Query().Get("secretPath"), strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/")
		if _, exists := f.secrets[path][key]; !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		secret := map[string]any{"id": key, "secretKey": key, "version": f.version(path, key)}
		if r.URL.Query().Get("viewSecretValue") == "true" {
			secret["secretValue"] = f.secrets[path][key]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secret": secret})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v4/secrets/"):
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		key := strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/")
		if _, exists := f.secrets[body["secretPath"]][key]; !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.approval {
			_ = json.NewEncoder(w).Encode(map[string]any{"approval": map[string]any{"id": "a-1"}})
			return
		}
		f.edit(body["secretPath"], key, body["secretValue"])
		f.updated = append(f.updated, body["secretPath"]+" "+key)
		_ = json.NewEncoder(w).Encode(map[string]any{"secret": map[string]any{"id": key}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *infisical) version(path, key string) int {
	return f.versions[path+" "+key] + 1
}

func (f *infisical) edit(path, key, value string) {
	f.secrets[path][key] = value
	f.versions[path+" "+key]++
}

func loggedIn(method string, body map[string]string) bool {
	switch method {
	case "universal-auth/login":
		return body["clientId"] == "id" && body["clientSecret"] == "secret"
	case "gcp-auth/login":
		return body["identityId"] == "identity-1" && body["jwt"] == fake.IDTokenFor("identity-1")
	}
	return false
}

func envSource(t *testing.T, kind string, options any, values ...*variablestorev1.EnvSourceValue) *variablestorev1.EnvSource {
	t.Helper()
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	return &variablestorev1.EnvSource{Kind: kind, Options: encoded, Values: values}
}

func infisicalSource(t *testing.T, host string, write envsource.WritePolicy, edits ...func(*envsource.InfisicalOptions)) *variablestorev1.EnvSource {
	t.Helper()
	options := envsource.InfisicalOptions{
		Project:     "p-1",
		Environment: "prod",
		Path:        "/",
		Host:        host,
		Write:       write,
		Auth: &envsource.InfisicalAuth{Universal: &envsource.UniversalAuth{
			ClientID:     envsource.Variable{Name: "INFISICAL_CLIENT_ID"},
			ClientSecret: envsource.Variable{Name: "INFISICAL_CLIENT_SECRET"},
		}},
	}
	for _, edit := range edits {
		edit(&options)
	}
	return envSource(t, "infisical", options)
}

func withIdentity(options *envsource.InfisicalOptions) {
	options.Auth = &envsource.InfisicalAuth{Identity: &envsource.IdentityAuth{IdentityID: "identity-1"}}
}

func execSource(t *testing.T, values ...*variablestorev1.EnvSourceValue) *variablestorev1.EnvSource {
	t.Helper()
	return envSource(t, "exec", envsource.ExecOptions{Command: []string{"op", "inject"}, Format: envsource.FormatJSON}, values...)
}

const (
	never   = envsource.WriteNever
	missing = envsource.WriteMissing
	values  = envsource.WriteValues
)

var builtin = &variablestorev1.EnvSource{Kind: envsource.Builtin}

func servedWithIdentity(t *testing.T) variablestorev1connect.VariableStoreServiceClient {
	t.Helper()
	provider := fake.NewProvider(fake.Options{})
	return serve(t, &variablestoreserver.Service{CallerNamesEnvSource: true, Source: variablestoreserver.FixedBackend{
		KeyValues:     provider.KeyValues(),
		Cipher:        provider.Cipher(),
		ProveIdentity: provider.ProveIdentity,
	}})
}

func servedToDeployAndConnector(t *testing.T) (deploy, connector variablestorev1connect.VariableStoreServiceClient) {
	t.Helper()
	provider := fake.NewProvider(fake.Options{})
	backend := variablestoreserver.FixedBackend{KeyValues: provider.KeyValues(), Cipher: provider.Cipher()}
	return serve(t, &variablestoreserver.Service{Source: backend, CallerNamesEnvSource: true}), serve(t, &variablestoreserver.Service{Source: backend})
}

func setValue(t *testing.T, variables variablestorev1connect.VariableStoreServiceClient, tier environmentv1.Tier, at *variablestorev1.Coordinate, value string) error {
	t.Helper()
	_, err := variables.SetValue(context.Background(), &variablestorev1.SetValueRequest{Tier: tier, Coordinate: at, Value: value})
	return err
}

func setCredentials(t *testing.T, variables variablestorev1connect.VariableStoreServiceClient, tier environmentv1.Tier) {
	t.Helper()
	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret"} {
		if err := setValue(t, variables, tier, cell(key), value); err != nil {
			t.Fatalf("SetValue(%s) = %v, want a credential ocel stores", key, err)
		}
	}
}

func syncEnvSource(variables variablestorev1connect.VariableStoreServiceClient, tier environmentv1.Tier, source *variablestorev1.EnvSource) (*variablestorev1.SyncEnvSourceResponse, error) {
	return variables.SyncEnvSource(context.Background(), &variablestorev1.SyncEnvSourceRequest{
		Tier:    tier,
		Slug:    slug,
		From:    &variablestorev1.SyncEnvSourceRequest_EnvSource{EnvSource: source},
		Folders: []string{"/web"},
	})
}

func syncRegistered(variables variablestorev1connect.VariableStoreServiceClient, tier environmentv1.Tier) (*variablestorev1.SyncEnvSourceResponse, error) {
	return variables.SyncEnvSource(context.Background(), &variablestorev1.SyncEnvSourceRequest{
		Tier: tier,
		Slug: slug,
		From: &variablestorev1.SyncEnvSourceRequest_Registered{Registered: &variablestorev1.RegisteredEnvSource{}},
	})
}

func TestADeploySyncReadsInfisicalWithTheCredentialOcelStores(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://prod"
	fake.secrets["/web"]["API_KEY"] = "web-key"
	production := environmentv1.Tier_TIER_PRODUCTION

	_, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "INFISICAL_CLIENT_ID") {
		t.Fatalf("SyncEnvSource() with no credential set = %v, want the credential named", err)
	}

	setCredentials(t, variables, production)
	synced, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never))
	if err != nil {
		t.Fatalf("SyncEnvSource() = %v", err)
	}
	status := synced.GetStatus()
	if status.GetEnvSource() != "infisical:p-1/prod" || !status.GetScheduled() || (status.GetCanCreate() || status.GetCanUpdate()) || status.GetLastSuccessAt() == 0 {
		t.Fatalf("status = %+v, want a scheduled, read-only Infisical source that was just read", status)
	}
	if synced.GetWritten() != 2 || len(synced.GetPresent()) != 2 {
		t.Fatalf("SyncEnvSource() = %+v, want the root's value and the app folder's both written", synced)
	}
	if len(status.GetLinks()) != 2 || !strings.Contains(status.GetLinks()[0].GetUrl(), "/organizations/org-1/") {
		t.Fatalf("links = %v, want one per folder read, the root among them", status.GetLinks())
	}

	listed, err := variables.ListValues(context.Background(), &variablestorev1.ListValuesRequest{Tier: production, Slug: slug})
	if err != nil {
		t.Fatal(err)
	}
	provenance := map[string]string{}
	for _, value := range listed.GetValues() {
		provenance[value.GetCoordinate().GetKey()] = value.GetEnvSource()
	}
	if provenance["DATABASE_URL"] != "infisical:p-1/prod" || provenance["INFISICAL_CLIENT_ID"] != "" {
		t.Fatalf("provenance = %v, want copied values named by their env source and the credential stored by ocel", provenance)
	}

	described, err := variables.DescribeEnvSource(context.Background(), &variablestorev1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || described.GetStatus().GetLastSuccessAt() != status.GetLastSuccessAt() {
		t.Fatalf("DescribeEnvSource() = %+v, %v, want the status the sync recorded", described, err)
	}
	if got := described.GetStatus().GetCredentials(); len(got) != 2 || got[0] != "INFISICAL_CLIENT_ID" {
		t.Fatalf("credentials = %v, want the two variables universal auth reads", got)
	}
}

func credentialRefusals(err error) map[string]*variablestorev1.CredentialRefusal {
	out := map[string]*variablestorev1.CredentialRefusal{}
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return out
	}
	for _, detail := range wire.Details() {
		if value, err := detail.Value(); err == nil {
			if refused, ok := value.(*variablestorev1.CredentialRefusal); ok {
				out[refused.GetVariable()] = refused
			}
		}
	}
	return out
}

func TestASyncRefusedForItsCredentialsNamesEachOneForTheCallerToSet(t *testing.T) {
	variables, _ := served(t)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION

	_, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never))
	refused := credentialRefusals(err)
	if len(refused) != 2 || !refused["INFISICAL_CLIENT_ID"].GetUnset() || !refused["INFISICAL_CLIENT_SECRET"].GetUnset() {
		t.Fatalf("SyncEnvSource() with no credential set = %v, want both credentials refused as unset", err)
	}

	if err := setValue(t, variables, production, cell("INFISICAL_CLIENT_ID"), "id"); err != nil {
		t.Fatal(err)
	}
	_, err = syncEnvSource(variables, production, infisicalSource(t, server.URL, never))
	if refused := credentialRefusals(err); len(refused) != 1 || refused["INFISICAL_CLIENT_SECRET"] == nil {
		t.Fatalf("SyncEnvSource() with the client secret unset = %v, want the client secret alone refused", err)
	}
}

func TestATierWithNoEnvSourceSyncedDescribesAsBuiltin(t *testing.T) {
	variables, _ := served(t)

	described, err := variables.DescribeEnvSource(context.Background(), &variablestorev1.DescribeEnvSourceRequest{Tier: environmentv1.Tier_TIER_PREVIEW, Slug: slug})
	if err != nil || described.GetStatus().GetEnvSource() != "builtin" || described.GetStatus().GetScheduled() {
		t.Fatalf("DescribeEnvSource() = %+v, %v, want builtin", described, err)
	}
}

func TestAValueTheEnvSourceOwnsIsRefusedToEveryOtherWriter(t *testing.T) {
	variables, provider := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://preview"
	preview := environmentv1.Tier_TIER_PREVIEW
	deployPreview(t, provider, "pr-12")
	setCredentials(t, variables, preview)
	if err := setValue(t, variables, preview, cell("LEFTOVER"), "set-before-the-env-source"); err != nil {
		t.Fatal(err)
	}
	if _, err := syncEnvSource(variables, preview, infisicalSource(t, server.URL, never)); err != nil {
		t.Fatalf("SyncEnvSource() = %v", err)
	}

	err := setValue(t, variables, preview, cell("DATABASE_URL"), "by-hand")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "infisical:p-1/prod") || !strings.Contains(err.Error(), "/organizations/org-1/") {
		t.Fatalf("SetValue() on a value the env source owns = %v, want it refused naming the env source and where to change it", err)
	}
	_, err = variables.DeleteValue(context.Background(), &variablestorev1.DeleteValueRequest{Tier: preview, Coordinate: cell("DATABASE_URL")})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("DeleteValue() on a value the env source owns = %v, want it refused", err)
	}
	_, err = variables.SetReference(context.Background(), &variablestorev1.SetReferenceRequest{Tier: preview, Coordinate: cell("NEW"), Target: &variablestorev1.Coordinate{Slug: "other", Key: "K"}})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SetReference() on a tier-wide value = %v, want it refused", err)
	}

	override := &variablestorev1.Coordinate{Slug: slug, Key: "DATABASE_URL", Environment: "pr-12"}
	if err := setValue(t, variables, preview, override, "postgres://pr-12"); err != nil {
		t.Fatalf("SetValue() for one named preview environment = %v, want an override ocel stores", err)
	}
	if err := setValue(t, variables, preview, cell("INFISICAL_CLIENT_SECRET"), "secret"); err != nil {
		t.Fatalf("SetValue() on the credential = %v, want it stored by ocel", err)
	}
	if leftover, err := variables.GetValue(context.Background(), &variablestorev1.GetValueRequest{Tier: preview, Coordinate: cell("LEFTOVER")}); err != nil || leftover.GetFound() {
		t.Fatalf("GetValue() of a value set before the env source found=%t, %v, want it removed by the sync, which is the one writer now", leftover.GetFound(), err)
	}

	back, err := syncEnvSource(variables, preview, builtin)
	if err != nil || back.GetStatus().GetEnvSource() != "builtin" {
		t.Fatalf("SyncEnvSource(builtin) = %+v, %v", back, err)
	}
	if err := setValue(t, variables, preview, cell("DATABASE_URL"), "by-hand"); err != nil {
		t.Fatalf("SetValue() once the tier is back on ocel's store = %v", err)
	}
}

func TestATierSwitchedBackToBuiltinKeepsEachCopiedValueAsOcelsOwn(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://prod"
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never)); err != nil {
		t.Fatal(err)
	}

	if _, err := syncEnvSource(variables, production, builtin); err != nil {
		t.Fatal(err)
	}
	got, err := variables.GetValue(context.Background(), &variablestorev1.GetValueRequest{Tier: production, Coordinate: cell("DATABASE_URL"), Reveal: true})
	if err != nil || got.GetValue() != "postgres://prod" || got.GetMetadata().GetEnvSource() != "" {
		t.Fatalf("GetValue(DATABASE_URL) after the switch back = %q from %q, %v, want the value kept and named as ocel's own", got.GetValue(), got.GetMetadata().GetEnvSource(), err)
	}
}

func TestAnExecEnvSourceIsWrittenAsTheOutputTheCallerReadAndIsNeverScheduled(t *testing.T) {
	variables, _ := served(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	exec := execSource(t,
		&variablestorev1.EnvSourceValue{Cell: &variablestorev1.Cell{Key: "TOKEN"}, Value: "t"},
		&variablestorev1.EnvSourceValue{Cell: &variablestorev1.Cell{Folder: "/web", Key: "API_KEY"}, Value: "k"},
	)
	synced, err := syncEnvSource(variables, production, exec)
	if err != nil {
		t.Fatalf("SyncEnvSource(exec) = %v", err)
	}
	if synced.GetStatus().GetEnvSource() != "exec" || synced.GetStatus().GetScheduled() || synced.GetWritten() != 2 {
		t.Fatalf("SyncEnvSource(exec) = %+v, want both values written and nothing scheduled", synced)
	}
	got, err := variables.GetValue(context.Background(), &variablestorev1.GetValueRequest{Tier: production, Coordinate: &variablestorev1.Coordinate{Slug: slug, Folder: "/web", Key: "API_KEY"}, Reveal: true})
	if err != nil || got.GetValue() != "k" || got.GetMetadata().GetEnvSource() != "exec" {
		t.Fatalf("GetValue() = %q from %q, %v", got.GetValue(), got.GetMetadata().GetEnvSource(), err)
	}
	if err := setValue(t, variables, production, cell("TOKEN"), "by-hand"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SetValue() on a value exec owns = %v, want it refused", err)
	}
}

func TestAnEnvSourceWritingMissingKeysCreatesOneThenCopiesItButNeverUpdatesOne(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, missing)); err != nil {
		t.Fatal(err)
	}

	created, err := variables.SetEnvSourceValue(context.Background(), &variablestorev1.SetEnvSourceValueRequest{
		Tier:        production,
		Coordinate:  &variablestorev1.Coordinate{Slug: slug, Folder: "/web", Key: "STRIPE_KEY"},
		Value:       "sk_live",
		Description: "The key Stripe signs with",
	})
	if err != nil {
		t.Fatalf("SetEnvSourceValue() = %v", err)
	}
	if created.GetMetadata().GetEnvSource() != "infisical:p-1/prod" || created.GetMetadata().GetVersion() != 1 {
		t.Fatalf("SetEnvSourceValue() = %+v, want the created key copied back", created)
	}
	if len(fake.created) != 1 || fake.created[0] != "/web STRIPE_KEY The key Stripe signs with" {
		t.Fatalf("created = %v", fake.created)
	}

	_, err = variables.SetEnvSourceValue(context.Background(), &variablestorev1.SetEnvSourceValueRequest{
		Tier:       production,
		Coordinate: &variablestorev1.Coordinate{Slug: slug, Folder: "/web", Key: "STRIPE_KEY"},
		Value:      "clobber",
	})
	if connect.CodeOf(err) != connect.CodeAlreadyExists || !strings.Contains(err.Error(), `"values"`) {
		t.Fatalf("SetEnvSourceValue() over a key the env source has = %v, want AlreadyExists naming write \"values\"", err)
	}
	if len(fake.updated) != 0 {
		t.Fatalf("updated = %v, want nothing overwritten under write missing", fake.updated)
	}
}

func setInEnvSource(variables variablestorev1connect.VariableStoreServiceClient, key, value string) (*variablestorev1.SetEnvSourceValueResponse, error) {
	return variables.SetEnvSourceValue(context.Background(), &variablestorev1.SetEnvSourceValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &variablestorev1.Coordinate{Slug: slug, Folder: "/web", Key: key},
		Value:      value,
	})
}

func TestAnEnvSourceWritingValuesUpdatesOneItHoldsThenCopiesIt(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/web"]["API_KEY"] = "old"
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	synced, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, values))
	if err != nil {
		t.Fatal(err)
	}
	if status := synced.GetStatus(); !status.GetCanCreate() || !status.GetCanUpdate() {
		t.Fatalf("status = %+v, want an env source ocel may create and update values in", status)
	}

	set, err := setInEnvSource(variables, "API_KEY", "new")
	if err != nil {
		t.Fatalf("SetEnvSourceValue() = %v", err)
	}
	if set.GetCreated() || set.GetMetadata().GetEnvSource() != "infisical:p-1/prod" {
		t.Fatalf("SetEnvSourceValue() = %+v, want an update copied back", set)
	}
	if !slices.Equal(fake.updated, []string{"/web API_KEY"}) || fake.secrets["/web"]["API_KEY"] != "new" {
		t.Fatalf("updated = %v, want API_KEY updated in place", fake.updated)
	}
	got, err := variables.GetValue(context.Background(), &variablestorev1.GetValueRequest{Tier: production, Coordinate: &variablestorev1.Coordinate{Slug: slug, Folder: "/web", Key: "API_KEY"}, Reveal: true})
	if err != nil || got.GetValue() != "new" {
		t.Fatalf("GetValue() = %q, %v, want the updated value synced straight back", got.GetValue(), err)
	}

	created, err := setInEnvSource(variables, "NEW_KEY", "v")
	if err != nil || !created.GetCreated() || len(fake.created) != 1 {
		t.Fatalf("SetEnvSourceValue(NEW_KEY) = %+v, %v, want a key the env source lacks created", created, err)
	}
}

func TestAnUpdateOfAValueEditedInTheEnvSourceSinceOcelCopiedItIsRefused(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/web"]["API_KEY"] = "copied"
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, values)); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.edit("/web", "API_KEY", "edited in infisical")
	fake.mu.Unlock()

	_, err := setInEnvSource(variables, "API_KEY", "mine")
	if connect.CodeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), "ocel env sync") {
		t.Fatalf("SetEnvSourceValue() over an edit made since the copy = %v, want Aborted pointing at ocel env sync", err)
	}
	if fake.secrets["/web"]["API_KEY"] != "edited in infisical" || len(fake.updated) != 0 {
		t.Fatalf("API_KEY = %q, updated %v, want the edit kept", fake.secrets["/web"]["API_KEY"], fake.updated)
	}
}

func TestAKeyTheEnvSourceGainedSinceOcelLastReadItIsNeverOverwritten(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, values)); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.edit("/web", "API_KEY", "added in infisical")
	fake.mu.Unlock()

	_, err := setInEnvSource(variables, "API_KEY", "mine")
	if connect.CodeOf(err) != connect.CodeAborted || !strings.Contains(err.Error(), "ocel env sync") {
		t.Fatalf("SetEnvSourceValue() of a key added since the copy = %v, want Aborted pointing at ocel env sync", err)
	}
	if fake.secrets["/web"]["API_KEY"] != "added in infisical" {
		t.Fatalf("API_KEY = %q, want the value added in Infisical kept", fake.secrets["/web"]["API_KEY"])
	}
}

func TestAnUpdateOfAKeyTheFolderNoLongerKeepsSaysWhereItMayLive(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/web"]["API_KEY"] = "copied"
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, values)); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	delete(fake.secrets["/web"], "API_KEY")
	fake.mu.Unlock()

	_, err := setInEnvSource(variables, "API_KEY", "mine")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "imported") || !strings.Contains(err.Error(), "ocel env sync") {
		t.Fatalf("SetEnvSourceValue() of a key the folder no longer keeps = %v, want FailedPrecondition naming an import and ocel env sync", err)
	}
	if _, kept := fake.secrets["/web"]["API_KEY"]; kept || len(fake.created) != 0 {
		t.Fatalf("secrets = %v, created %v, want nothing written", fake.secrets["/web"], fake.created)
	}
}

func TestAKeyADeployCreatedEmptyIsFilledInUnderWriteValues(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/web"]["STRIPE_KEY"] = ""
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, values)); err != nil {
		t.Fatal(err)
	}

	set, err := setInEnvSource(variables, "STRIPE_KEY", "sk_live")
	if err != nil || set.GetCreated() || fake.secrets["/web"]["STRIPE_KEY"] != "sk_live" {
		t.Fatalf("SetEnvSourceValue() = %+v, %v, STRIPE_KEY %q, want the empty key filled in", set, err, fake.secrets["/web"]["STRIPE_KEY"])
	}
}

func TestAnUpdateHeldForApprovalIsReportedAsWaiting(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/web"]["API_KEY"] = "old"
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, values)); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.approval = true
	fake.mu.Unlock()

	set, err := setInEnvSource(variables, "API_KEY", "new")
	if err != nil || !set.GetAwaitingApproval() {
		t.Fatalf("SetEnvSourceValue() under an approval policy = %+v, %v, want it reported as waiting", set, err)
	}
}

func TestAValueIsCreatedOnlyInAFolderTheDeployRegistered(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, missing)); err != nil {
		t.Fatal(err)
	}

	for _, folder := range []string{"/..", "/web/..", "/.", "/api"} {
		_, err := variables.SetEnvSourceValue(context.Background(), &variablestorev1.SetEnvSourceValueRequest{
			Tier:       production,
			Coordinate: &variablestorev1.Coordinate{Slug: slug, Folder: folder, Key: "PLANTED"},
			Value:      "v",
		})
		if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
			t.Errorf("SetEnvSourceValue() in folder %q = %v, want InvalidArgument", folder, err)
		}
	}
	if len(fake.created) != 0 {
		t.Fatalf("created = %v, want nothing written outside the registered folders", fake.created)
	}
}

func TestACredentialIsNeverCreatedInTheEnvSourceItLogsInTo(t *testing.T) {
	variables, _ := served(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, missing)); err != nil {
		t.Fatal(err)
	}

	_, err := variables.SetEnvSourceValue(context.Background(), &variablestorev1.SetEnvSourceValueRequest{Tier: production, Coordinate: cell("INFISICAL_CLIENT_SECRET"), Value: "v"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "ocel env set") {
		t.Fatalf("SetEnvSourceValue(INFISICAL_CLIENT_SECRET) = %v, want it refused pointing at ocel env set", err)
	}
	if len(fake.created) != 0 {
		t.Fatalf("created = %v, want nothing sent to the env source", fake.created)
	}
}

func TestAReadOnlyEnvSourceTakesNoWrite(t *testing.T) {
	variables, _ := served(t)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never)); err != nil {
		t.Fatal(err)
	}

	_, err := variables.SetEnvSourceValue(context.Background(), &variablestorev1.SetEnvSourceValueRequest{Tier: production, Coordinate: cell("NEW"), Value: "v"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "write") {
		t.Fatalf("SetEnvSourceValue() into a read-only env source = %v, want a refusal naming write", err)
	}
}

func TestAValueForANamedPreviewEnvironmentIsNeverCreatedInTheEnvSource(t *testing.T) {
	variables, _ := served(t)

	_, err := variables.SetEnvSourceValue(context.Background(), &variablestorev1.SetEnvSourceValueRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Coordinate: &variablestorev1.Coordinate{Slug: slug, Key: "NEW", Environment: "pr-12"},
		Value:      "v",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "ocel env set") {
		t.Fatalf("SetEnvSourceValue() for pr-12 = %v, want it refused pointing at ocel env set", err)
	}
}

func TestAnIdentityAuthOnATargetWithNoCloudIdentityIsRefusedBeforeItIsRegistered(t *testing.T) {
	variables, _ := served(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	_, err := syncEnvSource(variables, production, infisicalSource(t, "https://infisical.example.com", never, withIdentity))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "this target's cloud identity") || !strings.Contains(err.Error(), "universal") {
		t.Errorf("SyncEnvSource() with identity auth on a target with no cloud identity = %v, want a refusal naming universal auth instead", err)
	}
	described, err := variables.DescribeEnvSource(context.Background(), &variablestorev1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || described.GetStatus().GetEnvSource() != "builtin" {
		t.Errorf("DescribeEnvSource() after a refused env source = %+v, %v, want nothing registered for a scheduled sync to fail on every poll", described, err)
	}
}

func TestAnIdentityAuthLogsInAsTheTargetsOwnCloudIdentity(t *testing.T) {
	variables := servedWithIdentity(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://prod"

	synced, err := syncEnvSource(variables, environmentv1.Tier_TIER_PRODUCTION, infisicalSource(t, server.URL, never, withIdentity))
	if err != nil || synced.GetWritten() != 1 {
		t.Fatalf("SyncEnvSource() with identity auth = %+v, %v, want the value read as the target's own cloud identity", synced, err)
	}
	if len(fake.loggedIn) != 1 || !strings.Contains(fake.loggedIn[0], "gcp-auth") {
		t.Fatalf("logged in through %v, want the login the fake's ID token proof reaches", fake.loggedIn)
	}
	if described, err := variables.DescribeEnvSource(context.Background(), &variablestorev1.DescribeEnvSourceRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: slug}); err != nil || len(described.GetStatus().GetCredentials()) != 0 {
		t.Fatalf("DescribeEnvSource() = %+v, %v, want no stored credential for an identity login", described, err)
	}
}

func TestAConnectorCallerNamingItsOwnEnvSourceIsRefused(t *testing.T) {
	deploy, connector := servedToDeployAndConnector(t)
	stored, server := newInfisical(t)
	elsewhere, other := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, deploy, production)
	if err := setValue(t, deploy, production, cell("STRIPE_KEY"), "sk_live"); err != nil {
		t.Fatal(err)
	}
	if _, err := syncEnvSource(deploy, production, infisicalSource(t, server.URL, never)); err != nil {
		t.Fatalf("SyncEnvSource() on the deploy path = %v, want the descriptor a deploy names accepted", err)
	}

	otherCredential := infisicalSource(t, server.URL, never, func(options *envsource.InfisicalOptions) {
		options.Auth.Universal.ClientSecret = envsource.Variable{Name: "STRIPE_KEY"}
	})
	exec := execSource(t, &variablestorev1.EnvSourceValue{Cell: &variablestorev1.Cell{Key: "DATABASE_URL"}, Value: "postgres://mine"})
	for name, source := range map[string]*variablestorev1.EnvSource{
		"another host":                infisicalSource(t, other.URL, never),
		"another credential variable": otherCredential,
		"exec values":                 exec,
	} {
		_, err := syncEnvSource(connector, production, source)
		if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "deploy") {
			t.Errorf("SyncEnvSource() through a connector naming %s = %v, want it refused pointing at a deploy", name, err)
		}
	}
	if len(elsewhere.loggedIn) != 0 || len(stored.loggedIn) != 1 {
		t.Errorf("logins: named host %v, stored host %v, want no credential sent anywhere a connector caller named", elsewhere.loggedIn, stored.loggedIn)
	}
	described, err := connector.DescribeEnvSource(context.Background(), &variablestorev1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || len(described.GetStatus().GetLinks()) == 0 || !strings.HasPrefix(described.GetStatus().GetLinks()[0].GetUrl(), server.URL) {
		t.Errorf("DescribeEnvSource() = %+v, %v, want the registration the deploy stored left as it was", described, err)
	}
}

func TestAConnectorSyncsTheEnvSourceADeployRegistered(t *testing.T) {
	deploy, connector := servedToDeployAndConnector(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, deploy, production)
	if _, err := syncEnvSource(deploy, production, infisicalSource(t, server.URL, never)); err != nil {
		t.Fatal(err)
	}
	fake.secrets["/web"]["API_KEY"] = "added-since"

	synced, err := syncRegistered(connector, production)
	if err != nil || synced.GetWritten() != 1 || synced.GetStatus().GetEnvSource() != "infisical:p-1/prod" {
		t.Fatalf("SyncEnvSource(registered) through a connector = %+v, %v, want the value added since the deploy copied in", synced, err)
	}
}

func TestSyncingWhatIsRegisteredWhereNothingIsReadsAsBuiltin(t *testing.T) {
	_, connector := servedToDeployAndConnector(t)

	synced, err := syncRegistered(connector, environmentv1.Tier_TIER_PREVIEW)
	if err != nil || synced.GetStatus().GetEnvSource() != "builtin" {
		t.Fatalf("SyncEnvSource(registered) with nothing registered = %+v, %v, want builtin and nothing read", synced, err)
	}
}

func TestARegisteredExecEnvSourceIsReReadOnlyByADeploy(t *testing.T) {
	deploy, connector := servedToDeployAndConnector(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	exec := execSource(t, &variablestorev1.EnvSourceValue{Cell: &variablestorev1.Cell{Key: "TOKEN"}, Value: "t"})
	if _, err := syncEnvSource(deploy, production, exec); err != nil {
		t.Fatal(err)
	}

	_, err := syncRegistered(connector, production)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("SyncEnvSource(registered) of exec = %v, want it refused: only a deploy runs the command", err)
	}
	got, err := deploy.GetValue(context.Background(), &variablestorev1.GetValueRequest{Tier: production, Coordinate: cell("TOKEN")})
	if err != nil || !got.GetFound() {
		t.Fatalf("GetValue(TOKEN) found=%t, %v, want exec's value kept rather than removed by an empty read", got.GetFound(), err)
	}
}

func unreadable(t *testing.T, host string) *variablestorev1.EnvSource {
	return infisicalSource(t, host, never, func(options *envsource.InfisicalOptions) { options.Path = "/gone" })
}

func TestADeployWhoseNewEnvSourceCannotBeReadLeavesTheOneItReplacedSyncing(t *testing.T) {
	deploy, connector := servedToDeployAndConnector(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, deploy, production)
	if _, err := syncEnvSource(deploy, production, infisicalSource(t, server.URL, never)); err != nil {
		t.Fatal(err)
	}

	_, err := syncEnvSource(deploy, production, unreadable(t, server.URL))
	if err == nil || len(credentialRefusals(err)) != 0 {
		t.Fatalf("SyncEnvSource() of a path Infisical lacks = %v, want a read failure", err)
	}
	fake.secrets["/web"]["API_KEY"] = "rotated-since"
	synced, err := syncRegistered(connector, production)
	if err != nil || synced.GetWritten() != 1 {
		t.Fatalf("SyncEnvSource(registered) after the failed deploy = %+v, %v, want the working env source read and the rotated value copied", synced, err)
	}
}

func TestADeployWhoseFirstEnvSourceCannotBeReadLeavesTheTierBuiltin(t *testing.T) {
	variables, _ := served(t)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)

	if _, err := syncEnvSource(variables, production, unreadable(t, server.URL)); err == nil {
		t.Fatal("SyncEnvSource() of a path Infisical lacks = nil, want a read failure")
	}
	described, err := variables.DescribeEnvSource(context.Background(), &variablestorev1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || described.GetStatus().GetEnvSource() != "builtin" {
		t.Fatalf("DescribeEnvSource() = %+v, %v, want builtin: nothing was ever read from the env source", described, err)
	}
	if err := setValue(t, variables, production, cell("DATABASE_URL"), "postgres://mine"); err != nil {
		t.Fatalf("SetValue() after the failed deploy = %v, want ocel's own value taken", err)
	}
}

func TestADeployWhoseNewEnvSourceLacksItsCredentialRegistersItSoTheCredentialCanBeSet(t *testing.T) {
	variables, _ := served(t)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, variables, production)
	if _, err := syncEnvSource(variables, production, infisicalSource(t, server.URL, never)); err != nil {
		t.Fatal(err)
	}
	renamed := infisicalSource(t, server.URL, never, func(options *envsource.InfisicalOptions) {
		options.Auth.Universal.ClientID = envsource.Variable{Name: "OTHER_CLIENT_ID"}
	})

	_, err := syncEnvSource(variables, production, renamed)
	if refused := credentialRefusals(err); refused["OTHER_CLIENT_ID"] == nil {
		t.Fatalf("SyncEnvSource() with OTHER_CLIENT_ID unset = %v, want it refused for the caller to set", err)
	}
	if err := setValue(t, variables, production, cell("OTHER_CLIENT_ID"), "id"); err != nil {
		t.Fatalf("SetValue(OTHER_CLIENT_ID) = %v, want the credential the new env source logs in with taken", err)
	}
	if _, err := syncEnvSource(variables, production, renamed); err != nil {
		t.Fatalf("SyncEnvSource() once its credential is set = %v", err)
	}
}

func TestAnEnvSourceTheCallerNamesIsDecodedBeforeAnythingIsRegistered(t *testing.T) {
	variables, _ := served(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	for name, c := range map[string]struct {
		source *variablestorev1.EnvSource
		want   string
	}{
		"a kind ocel does not know":    {&variablestorev1.EnvSource{Kind: "vault", Options: []byte(`{}`)}, `"vault"`},
		"dotenv, which only dev reads": {&variablestorev1.EnvSource{Kind: envsource.Dotenv}, "dotenv"},
		"options that are no JSON":     {&variablestorev1.EnvSource{Kind: "infisical", Options: []byte(`{"project":`)}, "infisical"},
		"options with data after them": {&variablestorev1.EnvSource{Kind: "exec", Options: []byte(`{"command":["op"],"format":"json"}garbage`)}, "exec"},
		"infisical with no project": {infisicalSource(t, "https://infisical.example.com", never, func(options *envsource.InfisicalOptions) {
			options.Project = ""
		}), "project"},
		"infisical with no auth": {infisicalSource(t, "https://infisical.example.com", never, func(options *envsource.InfisicalOptions) {
			options.Auth = nil
		}), "auth"},
		"infisical writing a way ocel does not know": {infisicalSource(t, "https://infisical.example.com", "always"), "write"},
		"exec with no command":                       {envSource(t, "exec", envsource.ExecOptions{Format: envsource.FormatJSON}), "command"},
		"infisical sent with values a deploy read": {&variablestorev1.EnvSource{
			Kind:    "infisical",
			Options: infisicalSource(t, "https://infisical.example.com", never).GetOptions(),
			Values:  []*variablestorev1.EnvSourceValue{{Cell: &variablestorev1.Cell{Key: "TOKEN"}, Value: "t"}},
		}, "values"},
	} {
		_, err := syncEnvSource(variables, production, c.source)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), c.want) {
			t.Errorf("SyncEnvSource() naming %s = %v, want it refused as invalid naming %s", name, err, c.want)
		}
	}
	described, err := variables.DescribeEnvSource(context.Background(), &variablestorev1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || described.GetStatus().GetEnvSource() != "builtin" {
		t.Errorf("DescribeEnvSource() after every refusal = %+v, %v, want nothing registered", described, err)
	}
}
