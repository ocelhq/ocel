package envvarsserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envvarsserver"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type infisical struct {
	mu       sync.Mutex
	secrets  map[string]map[string]string
	created  []string
	loggedIn []string
}

func newInfisical(t *testing.T) (*infisical, *httptest.Server) {
	t.Helper()
	fake := &infisical{secrets: map[string]map[string]string{"/": {}, "/web": {}}}
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
			secrets = append(secrets, map[string]any{"id": key, "secretKey": key, "secretValue": value, "version": len(value)})
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
	default:
		w.WriteHeader(http.StatusNotFound)
	}
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

func infisicalSource(host string, writeMissing bool) *envvarsv1.EnvSource {
	return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Infisical{Infisical: &envvarsv1.InfisicalEnvSource{
		Project:      "p-1",
		Environment:  "prod",
		Path:         "/",
		Host:         host,
		WriteMissing: writeMissing,
		Auth: &envvarsv1.InfisicalAuth{Method: &envvarsv1.InfisicalAuth_Universal{Universal: &envvarsv1.InfisicalUniversalAuth{
			ClientIdVariable:     "INFISICAL_CLIENT_ID",
			ClientSecretVariable: "INFISICAL_CLIENT_SECRET",
		}}},
	}}}
}

func identitySource(host string, auth *envvarsv1.InfisicalAuth) *envvarsv1.EnvSource {
	source := infisicalSource(host, false)
	source.GetInfisical().Auth = auth
	return source
}

var identityAuth = &envvarsv1.InfisicalAuth{Method: &envvarsv1.InfisicalAuth_Identity{Identity: &envvarsv1.InfisicalIdentityAuth{IdentityId: "identity-1"}}}

func servedWithIdentity(t *testing.T) envvarsv1connect.EnvVarsServiceClient {
	t.Helper()
	provider := fake.NewProvider(fake.Options{})
	return serve(t, &envvarsserver.Service{CallerNamesEnvSource: true, Source: envvarsserver.FixedBackend{
		Records:       provider.Records(),
		Cipher:        provider.Cipher(),
		ProveIdentity: provider.ProveIdentity,
	}})
}

func servedToDeployAndConnector(t *testing.T) (deploy, connector envvarsv1connect.EnvVarsServiceClient) {
	t.Helper()
	provider := fake.NewProvider(fake.Options{})
	backend := envvarsserver.FixedBackend{Records: provider.Records(), Cipher: provider.Cipher()}
	return serve(t, &envvarsserver.Service{Source: backend, CallerNamesEnvSource: true}), serve(t, &envvarsserver.Service{Source: backend})
}

func setValue(t *testing.T, vars envvarsv1connect.EnvVarsServiceClient, tier environmentv1.Tier, at *envvarsv1.Coordinate, value string) error {
	t.Helper()
	_, err := vars.SetValue(context.Background(), &envvarsv1.SetValueRequest{Tier: tier, Coordinate: at, Value: value})
	return err
}

func setCredentials(t *testing.T, vars envvarsv1connect.EnvVarsServiceClient, tier environmentv1.Tier) {
	t.Helper()
	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret"} {
		if err := setValue(t, vars, tier, cell(key), value); err != nil {
			t.Fatalf("SetValue(%s) = %v, want a credential ocel stores", key, err)
		}
	}
}

func syncEnvSource(vars envvarsv1connect.EnvVarsServiceClient, tier environmentv1.Tier, source *envvarsv1.EnvSource) (*envvarsv1.SyncEnvSourceResponse, error) {
	return vars.SyncEnvSource(context.Background(), &envvarsv1.SyncEnvSourceRequest{
		Tier:    tier,
		Slug:    slug,
		From:    &envvarsv1.SyncEnvSourceRequest_EnvSource{EnvSource: source},
		Folders: []string{"/web"},
	})
}

func syncRegistered(vars envvarsv1connect.EnvVarsServiceClient, tier environmentv1.Tier) (*envvarsv1.SyncEnvSourceResponse, error) {
	return vars.SyncEnvSource(context.Background(), &envvarsv1.SyncEnvSourceRequest{
		Tier: tier,
		Slug: slug,
		From: &envvarsv1.SyncEnvSourceRequest_Registered{Registered: &envvarsv1.RegisteredEnvSource{}},
	})
}

func TestADeploySyncReadsInfisicalWithTheCredentialOcelStores(t *testing.T) {
	vars, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://prod"
	fake.secrets["/web"]["API_KEY"] = "web-key"
	production := environmentv1.Tier_TIER_PRODUCTION

	_, err := syncEnvSource(vars, production, infisicalSource(server.URL, false))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "INFISICAL_CLIENT_ID") {
		t.Fatalf("SyncEnvSource() with no credential set = %v, want the credential named", err)
	}

	setCredentials(t, vars, production)
	synced, err := syncEnvSource(vars, production, infisicalSource(server.URL, false))
	if err != nil {
		t.Fatalf("SyncEnvSource() = %v", err)
	}
	status := synced.GetStatus()
	if status.GetEnvSource() != "infisical:p-1/prod" || !status.GetScheduled() || status.GetWritable() || status.GetLastSuccessAt() == 0 {
		t.Fatalf("status = %+v, want a scheduled, read-only Infisical source that was just read", status)
	}
	if synced.GetWritten() != 2 || len(synced.GetPresent()) != 2 {
		t.Fatalf("SyncEnvSource() = %+v, want the root's value and the app folder's both written", synced)
	}
	if len(status.GetLinks()) != 2 || !strings.Contains(status.GetLinks()[0].GetUrl(), "/organizations/org-1/") {
		t.Fatalf("links = %v, want one per folder read, the root among them", status.GetLinks())
	}

	listed, err := vars.ListValues(context.Background(), &envvarsv1.ListValuesRequest{Tier: production, Slug: slug})
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

	described, err := vars.DescribeEnvSource(context.Background(), &envvarsv1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || described.GetStatus().GetLastSuccessAt() != status.GetLastSuccessAt() {
		t.Fatalf("DescribeEnvSource() = %+v, %v, want the status the sync recorded", described, err)
	}
	if got := described.GetStatus().GetCredentials(); len(got) != 2 || got[0] != "INFISICAL_CLIENT_ID" {
		t.Fatalf("credentials = %v, want the two variables universal auth reads", got)
	}
}

func TestATierWithNoEnvSourceSyncedDescribesAsBuiltin(t *testing.T) {
	vars, _ := served(t)

	described, err := vars.DescribeEnvSource(context.Background(), &envvarsv1.DescribeEnvSourceRequest{Tier: environmentv1.Tier_TIER_PREVIEW, Slug: slug})
	if err != nil || described.GetStatus().GetEnvSource() != "builtin" || described.GetStatus().GetScheduled() {
		t.Fatalf("DescribeEnvSource() = %+v, %v, want builtin", described, err)
	}
}

func TestAValueTheEnvSourceOwnsIsRefusedToEveryOtherWriter(t *testing.T) {
	vars, provider := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://preview"
	preview := environmentv1.Tier_TIER_PREVIEW
	deployPreview(t, provider, "pr-12")
	setCredentials(t, vars, preview)
	if err := setValue(t, vars, preview, cell("LEFTOVER"), "set-before-the-env-source"); err != nil {
		t.Fatal(err)
	}
	if _, err := syncEnvSource(vars, preview, infisicalSource(server.URL, false)); err != nil {
		t.Fatalf("SyncEnvSource() = %v", err)
	}

	err := setValue(t, vars, preview, cell("DATABASE_URL"), "by-hand")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "infisical:p-1/prod") || !strings.Contains(err.Error(), "/organizations/org-1/") {
		t.Fatalf("SetValue() on a value the env source owns = %v, want it refused naming the env source and where to change it", err)
	}
	_, err = vars.DeleteValue(context.Background(), &envvarsv1.DeleteValueRequest{Tier: preview, Coordinate: cell("DATABASE_URL")})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("DeleteValue() on a value the env source owns = %v, want it refused", err)
	}
	_, err = vars.SetReference(context.Background(), &envvarsv1.SetReferenceRequest{Tier: preview, Coordinate: cell("NEW"), Target: &envvarsv1.Coordinate{Slug: "other", Key: "K"}})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SetReference() on a class-wide value = %v, want it refused", err)
	}

	override := &envvarsv1.Coordinate{Slug: slug, Key: "DATABASE_URL", Environment: "pr-12"}
	if err := setValue(t, vars, preview, override, "postgres://pr-12"); err != nil {
		t.Fatalf("SetValue() for one named preview environment = %v, want an override ocel stores", err)
	}
	if err := setValue(t, vars, preview, cell("INFISICAL_CLIENT_SECRET"), "secret"); err != nil {
		t.Fatalf("SetValue() on the credential = %v, want it stored by ocel", err)
	}
	if leftover, err := vars.GetValue(context.Background(), &envvarsv1.GetValueRequest{Tier: preview, Coordinate: cell("LEFTOVER")}); err != nil || leftover.GetFound() {
		t.Fatalf("GetValue() of a value set before the env source found=%t, %v, want it removed by the sync, which is the one writer now", leftover.GetFound(), err)
	}

	builtin := &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Builtin{Builtin: &envvarsv1.BuiltinEnvSource{}}}
	back, err := syncEnvSource(vars, preview, builtin)
	if err != nil || back.GetStatus().GetEnvSource() != "builtin" {
		t.Fatalf("SyncEnvSource(builtin) = %+v, %v", back, err)
	}
	if err := setValue(t, vars, preview, cell("DATABASE_URL"), "by-hand"); err != nil {
		t.Fatalf("SetValue() once the tier is back on ocel's store = %v", err)
	}
}

func TestATierSwitchedBackToBuiltinKeepsEachCopiedValueAsOcelsOwn(t *testing.T) {
	vars, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://prod"
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, vars, production)
	if _, err := syncEnvSource(vars, production, infisicalSource(server.URL, false)); err != nil {
		t.Fatal(err)
	}

	builtin := &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Builtin{Builtin: &envvarsv1.BuiltinEnvSource{}}}
	if _, err := syncEnvSource(vars, production, builtin); err != nil {
		t.Fatal(err)
	}
	got, err := vars.GetValue(context.Background(), &envvarsv1.GetValueRequest{Tier: production, Coordinate: cell("DATABASE_URL"), Reveal: true})
	if err != nil || got.GetValue() != "postgres://prod" || got.GetMetadata().GetEnvSource() != "" {
		t.Fatalf("GetValue(DATABASE_URL) after the switch back = %q from %q, %v, want the value kept and named as ocel's own", got.GetValue(), got.GetMetadata().GetEnvSource(), err)
	}
}

func TestAnExecEnvSourceIsWrittenAsTheOutputTheCallerReadAndIsNeverScheduled(t *testing.T) {
	vars, _ := served(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	exec := &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Exec{Exec: &envvarsv1.ExecEnvSource{
		Command: []string{"op", "inject"},
		Values: []*envvarsv1.EnvSourceValue{
			{Cell: &envvarsv1.Cell{Key: "TOKEN"}, Value: "t", Version: "sha-1"},
			{Cell: &envvarsv1.Cell{Folder: "/web", Key: "API_KEY"}, Value: "k", Version: "sha-2"},
		},
	}}}
	synced, err := syncEnvSource(vars, production, exec)
	if err != nil {
		t.Fatalf("SyncEnvSource(exec) = %v", err)
	}
	if synced.GetStatus().GetEnvSource() != "exec" || synced.GetStatus().GetScheduled() || synced.GetWritten() != 2 {
		t.Fatalf("SyncEnvSource(exec) = %+v, want both values written and nothing scheduled", synced)
	}
	got, err := vars.GetValue(context.Background(), &envvarsv1.GetValueRequest{Tier: production, Coordinate: &envvarsv1.Coordinate{Slug: slug, Folder: "/web", Key: "API_KEY"}, Reveal: true})
	if err != nil || got.GetValue() != "k" || got.GetMetadata().GetEnvSource() != "exec" {
		t.Fatalf("GetValue() = %q from %q, %v", got.GetValue(), got.GetMetadata().GetEnvSource(), err)
	}
	if err := setValue(t, vars, production, cell("TOKEN"), "by-hand"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SetValue() on a value exec owns = %v, want it refused", err)
	}
}

func TestAWritableEnvSourceCreatesAMissingKeyThenCopiesIt(t *testing.T) {
	vars, _ := served(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, vars, production)
	if _, err := syncEnvSource(vars, production, infisicalSource(server.URL, true)); err != nil {
		t.Fatal(err)
	}

	created, err := vars.CreateEnvSourceValue(context.Background(), &envvarsv1.CreateEnvSourceValueRequest{
		Tier:        production,
		Coordinate:  &envvarsv1.Coordinate{Slug: slug, Folder: "/web", Key: "STRIPE_KEY"},
		Value:       "sk_live",
		Description: "The key Stripe signs with",
	})
	if err != nil {
		t.Fatalf("CreateEnvSourceValue() = %v", err)
	}
	if created.GetMetadata().GetEnvSource() != "infisical:p-1/prod" || created.GetMetadata().GetVersion() != 1 {
		t.Fatalf("CreateEnvSourceValue() = %+v, want the created key copied back", created)
	}
	if len(fake.created) != 1 || fake.created[0] != "/web STRIPE_KEY The key Stripe signs with" {
		t.Fatalf("created = %v", fake.created)
	}

	_, err = vars.CreateEnvSourceValue(context.Background(), &envvarsv1.CreateEnvSourceValueRequest{
		Tier:       production,
		Coordinate: &envvarsv1.Coordinate{Slug: slug, Folder: "/web", Key: "STRIPE_KEY"},
		Value:      "clobber",
	})
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("CreateEnvSourceValue() over a key the env source has = %v, want AlreadyExists", err)
	}
}

func TestAValueIsCreatedOnlyInAFolderTheDeployRegistered(t *testing.T) {
	vars, _ := served(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, vars, production)
	if _, err := syncEnvSource(vars, production, infisicalSource(server.URL, true)); err != nil {
		t.Fatal(err)
	}

	for _, folder := range []string{"/..", "/web/..", "/.", "/api"} {
		_, err := vars.CreateEnvSourceValue(context.Background(), &envvarsv1.CreateEnvSourceValueRequest{
			Tier:       production,
			Coordinate: &envvarsv1.Coordinate{Slug: slug, Folder: folder, Key: "PLANTED"},
			Value:      "v",
		})
		if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
			t.Errorf("CreateEnvSourceValue() in folder %q = %v, want InvalidArgument", folder, err)
		}
	}
	if len(fake.created) != 0 {
		t.Fatalf("created = %v, want nothing written outside the registered folders", fake.created)
	}
}

func TestACredentialIsNeverCreatedInTheEnvSourceItLogsInTo(t *testing.T) {
	vars, _ := served(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, vars, production)
	if _, err := syncEnvSource(vars, production, infisicalSource(server.URL, true)); err != nil {
		t.Fatal(err)
	}

	_, err := vars.CreateEnvSourceValue(context.Background(), &envvarsv1.CreateEnvSourceValueRequest{Tier: production, Coordinate: cell("INFISICAL_CLIENT_SECRET"), Value: "v"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "ocel env set") {
		t.Fatalf("CreateEnvSourceValue(INFISICAL_CLIENT_SECRET) = %v, want it refused pointing at ocel env set", err)
	}
	if len(fake.created) != 0 {
		t.Fatalf("created = %v, want nothing sent to the env source", fake.created)
	}
}

func TestAReadOnlyEnvSourceTakesNoWrite(t *testing.T) {
	vars, _ := served(t)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, vars, production)
	if _, err := syncEnvSource(vars, production, infisicalSource(server.URL, false)); err != nil {
		t.Fatal(err)
	}

	_, err := vars.CreateEnvSourceValue(context.Background(), &envvarsv1.CreateEnvSourceValueRequest{Tier: production, Coordinate: cell("NEW"), Value: "v"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "write") {
		t.Fatalf("CreateEnvSourceValue() into a read-only env source = %v, want a refusal naming write", err)
	}
}

func TestAValueForANamedPreviewEnvironmentIsNeverCreatedInTheEnvSource(t *testing.T) {
	vars, _ := served(t)

	_, err := vars.CreateEnvSourceValue(context.Background(), &envvarsv1.CreateEnvSourceValueRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Coordinate: &envvarsv1.Coordinate{Slug: slug, Key: "NEW", Environment: "pr-12"},
		Value:      "v",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "ocel env set") {
		t.Fatalf("CreateEnvSourceValue() for pr-12 = %v, want it refused pointing at ocel env set", err)
	}
}

func TestAnIdentityAuthOnATargetWithNoCloudIdentityIsRefusedBeforeItIsRegistered(t *testing.T) {
	vars, _ := served(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	_, err := syncEnvSource(vars, production, identitySource("https://infisical.example.com", identityAuth))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "this target's cloud identity") || !strings.Contains(err.Error(), "universal") {
		t.Errorf("SyncEnvSource() with identity auth on a target with no cloud identity = %v, want a refusal naming universal auth instead", err)
	}
	described, err := vars.DescribeEnvSource(context.Background(), &envvarsv1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || described.GetStatus().GetEnvSource() != "builtin" {
		t.Errorf("DescribeEnvSource() after a refused env source = %+v, %v, want nothing registered for a scheduled sync to fail on every poll", described, err)
	}
}

func TestAnIdentityAuthLogsInAsTheTargetsOwnCloudIdentity(t *testing.T) {
	vars := servedWithIdentity(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://prod"

	synced, err := syncEnvSource(vars, environmentv1.Tier_TIER_PRODUCTION, identitySource(server.URL, identityAuth))
	if err != nil || synced.GetWritten() != 1 {
		t.Fatalf("SyncEnvSource() with identity auth = %+v, %v, want the value read as the target's own cloud identity", synced, err)
	}
	if len(fake.loggedIn) != 1 || !strings.Contains(fake.loggedIn[0], "gcp-auth") {
		t.Fatalf("logged in through %v, want the login the fake's ID token proof reaches", fake.loggedIn)
	}
	if described, err := vars.DescribeEnvSource(context.Background(), &envvarsv1.DescribeEnvSourceRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: slug}); err != nil || len(described.GetStatus().GetCredentials()) != 0 {
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
	if _, err := syncEnvSource(deploy, production, infisicalSource(server.URL, false)); err != nil {
		t.Fatalf("SyncEnvSource() on the deploy path = %v, want the descriptor a deploy names accepted", err)
	}

	otherCredential := infisicalSource(server.URL, false)
	otherCredential.GetInfisical().GetAuth().GetUniversal().ClientSecretVariable = "STRIPE_KEY"
	exec := &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Exec{Exec: &envvarsv1.ExecEnvSource{
		Command: []string{"op"},
		Values:  []*envvarsv1.EnvSourceValue{{Cell: &envvarsv1.Cell{Key: "DATABASE_URL"}, Value: "postgres://mine"}},
	}}}
	for name, source := range map[string]*envvarsv1.EnvSource{
		"another host":                infisicalSource(other.URL, false),
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
	described, err := connector.DescribeEnvSource(context.Background(), &envvarsv1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || len(described.GetStatus().GetLinks()) == 0 || !strings.HasPrefix(described.GetStatus().GetLinks()[0].GetUrl(), server.URL) {
		t.Errorf("DescribeEnvSource() = %+v, %v, want the registration the deploy stored left as it was", described, err)
	}
}

func TestAConnectorSyncsTheEnvSourceADeployRegistered(t *testing.T) {
	deploy, connector := servedToDeployAndConnector(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	setCredentials(t, deploy, production)
	if _, err := syncEnvSource(deploy, production, infisicalSource(server.URL, false)); err != nil {
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
	exec := &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Exec{Exec: &envvarsv1.ExecEnvSource{
		Command: []string{"op"},
		Values:  []*envvarsv1.EnvSourceValue{{Cell: &envvarsv1.Cell{Key: "TOKEN"}, Value: "t", Version: "sha-1"}},
	}}}
	if _, err := syncEnvSource(deploy, production, exec); err != nil {
		t.Fatal(err)
	}

	_, err := syncRegistered(connector, production)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("SyncEnvSource(registered) of exec = %v, want it refused: only a deploy runs the command", err)
	}
	got, err := deploy.GetValue(context.Background(), &envvarsv1.GetValueRequest{Tier: production, Coordinate: cell("TOKEN")})
	if err != nil || !got.GetFound() {
		t.Fatalf("GetValue(TOKEN) found=%t, %v, want exec's value kept rather than removed by an empty read", got.GetFound(), err)
	}
}
