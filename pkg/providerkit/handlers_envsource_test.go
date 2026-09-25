package providerkit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

type infisical struct {
	mu      sync.Mutex
	secrets map[string]map[string]string
	created []string
}

func newInfisical(t *testing.T) (*infisical, *httptest.Server) {
	fake := &infisical{secrets: map[string]map[string]string{"/": {}, "/web": {}}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return fake, server
}

func (f *infisical) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/v1/auth/universal-auth/login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["clientId"] != "id" || body["clientSecret"] != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Invalid credentials"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expiresIn": 3600})
	case r.Header.Get("Authorization") != "Bearer token":
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/api/v1/projects/p-1":
		_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"orgId": "org-1"}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v4/secrets":
		folder, held := f.secrets[r.URL.Query().Get("secretPath")]
		if !held {
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

func infisicalSource(host string, writeMissing bool) *envvarsv1.EnvSource {
	return &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Infisical{Infisical: &envvarsv1.InfisicalEnvSource{
		Project:      "p-1",
		Environment:  "prod",
		Path:         "/",
		Host:         host,
		WriteMissing: writeMissing,
		Auth: &envvarsv1.InfisicalAuth{Method: &envvarsv1.InfisicalAuth_Universal{Universal: &envvarsv1.InfisicalUniversalAuth{
			ClientIdVar:     "INFISICAL_CLIENT_ID",
			ClientSecretVar: "INFISICAL_CLIENT_SECRET",
		}}},
	}}}
}

func setValue(t *testing.T, vars envvarsv1connect.EnvVarsServiceClient, tier environmentv1.Tier, at *envvarsv1.Coordinate, value string) error {
	t.Helper()
	_, err := vars.SetValue(context.Background(), &envvarsv1.SetValueRequest{Tier: tier, Coordinate: at, Value: value})
	return err
}

func syncSource(vars envvarsv1connect.EnvVarsServiceClient, tier environmentv1.Tier, source *envvarsv1.EnvSource) (*envvarsv1.SyncEnvSourceResponse, error) {
	return vars.SyncEnvSource(context.Background(), &envvarsv1.SyncEnvSourceRequest{Tier: tier, Slug: slug, EnvSource: source, Folders: []string{"", "/web"}})
}

func TestADeploySyncReadsInfisicalAsTheCredentialOcelHolds(t *testing.T) {
	vars, _ := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://prod"
	fake.secrets["/web"]["API_KEY"] = "web-key"
	production := environmentv1.Tier_TIER_PRODUCTION

	_, err := syncSource(vars, production, infisicalSource(server.URL, false))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "INFISICAL_CLIENT_ID") {
		t.Fatalf("SyncEnvSource() with no credential set = %v, want the credential named", err)
	}

	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret"} {
		if err := setValue(t, vars, production, cell(key), value); err != nil {
			t.Fatalf("SetValue(%s) = %v, want a credential ocel's own to set", key, err)
		}
	}
	synced, err := syncSource(vars, production, infisicalSource(server.URL, false))
	if err != nil {
		t.Fatalf("SyncEnvSource() = %v", err)
	}
	status := synced.GetStatus()
	if status.GetEnvSource() != "infisical:p-1/prod" || !status.GetStanding() || status.GetWritable() || status.GetLastSuccessAt() == 0 {
		t.Fatalf("status = %+v", status)
	}
	if synced.GetWritten() != 2 || len(synced.GetPresent()) != 2 {
		t.Fatalf("SyncEnvSource() = %+v, want both values written", synced)
	}
	if len(status.GetLinks()) != 2 || !strings.Contains(status.GetLinks()[0].GetLink(), "/organizations/org-1/") {
		t.Fatalf("links = %v, want one per folder read", status.GetLinks())
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
		t.Fatalf("provenance = %v, want mirrored values named by their source and the credential ocel's own", provenance)
	}

	described, err := vars.DescribeEnvSource(context.Background(), &envvarsv1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
	if err != nil || described.GetStatus().GetLastSuccessAt() != status.GetLastSuccessAt() {
		t.Fatalf("DescribeEnvSource() = %+v, %v", described, err)
	}
	if got := described.GetStatus().GetCredentials(); len(got) != 2 || got[0] != "INFISICAL_CLIENT_ID" {
		t.Fatalf("credentials = %v", got)
	}
}

func TestAValueTheSourceOwnsIsRefusedToEveryWriterButTheSource(t *testing.T) {
	vars, provider := served(t)
	fake, server := newInfisical(t)
	fake.secrets["/"]["DATABASE_URL"] = "postgres://preview"
	preview := environmentv1.Tier_TIER_PREVIEW
	deployPreview(t, provider, "pr-12")
	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret", "LEFTOVER": "set-before-the-source"} {
		if err := setValue(t, vars, preview, cell(key), value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := syncSource(vars, preview, infisicalSource(server.URL, false)); err != nil {
		t.Fatalf("SyncEnvSource() = %v", err)
	}

	err := setValue(t, vars, preview, cell("DATABASE_URL"), "by-hand")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "infisical:p-1/prod") || !strings.Contains(err.Error(), "/organizations/org-1/") {
		t.Fatalf("SetValue() on a cell the source owns = %v, want it refused naming the source and where to change it", err)
	}
	_, err = vars.DeleteValue(context.Background(), &envvarsv1.DeleteValueRequest{Tier: preview, Coordinate: cell("DATABASE_URL")})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("DeleteValue() on a cell the source owns = %v, want it refused", err)
	}
	_, err = vars.SetReference(context.Background(), &envvarsv1.SetReferenceRequest{Tier: preview, Coordinate: cell("NEW"), Target: &envvarsv1.Coordinate{Slug: "other", Key: "K"}})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SetReference() on a cell the source owns = %v, want it refused", err)
	}

	override := &envvarsv1.Coordinate{Slug: slug, Key: "DATABASE_URL", Environment: "pr-12"}
	if err := setValue(t, vars, preview, override, "postgres://pr-12"); err != nil {
		t.Fatalf("SetValue() for one named preview = %v, want an override ocel holds", err)
	}
	if err := setValue(t, vars, preview, cell("INFISICAL_CLIENT_SECRET"), "secret"); err != nil {
		t.Fatalf("SetValue() on the credential = %v, want it ocel's own", err)
	}
	if _, err := vars.DeleteValue(context.Background(), &envvarsv1.DeleteValueRequest{Tier: preview, Coordinate: cell("LEFTOVER")}); err != nil {
		t.Fatalf("DeleteValue() of a value set before the source = %v, want it cleared", err)
	}

	builtin := &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Builtin{Builtin: &envvarsv1.BuiltinEnvSource{}}}
	back, err := syncSource(vars, preview, builtin)
	if err != nil || back.GetStatus().GetEnvSource() != "builtin" {
		t.Fatalf("SyncEnvSource(builtin) = %+v, %v", back, err)
	}
	if err := setValue(t, vars, preview, cell("DATABASE_URL"), "by-hand"); err != nil {
		t.Fatalf("SetValue() once the tier is back on ocel's store = %v", err)
	}
}

func TestAnExecSourceIsWrittenAsItsOutputAndNeverStands(t *testing.T) {
	vars, _ := served(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	exec := &envvarsv1.EnvSource{Kind: &envvarsv1.EnvSource_Exec{Exec: &envvarsv1.ExecEnvSource{
		Command: []string{"op", "inject"},
		Values: []*envvarsv1.SourcedValue{
			{Key: "TOKEN", Value: "t", Version: "sha-1"},
			{Folder: "/web", Key: "API_KEY", Value: "k", Version: "sha-2"},
		},
	}}}
	synced, err := syncSource(vars, production, exec)
	if err != nil {
		t.Fatalf("SyncEnvSource(exec) = %v", err)
	}
	if synced.GetStatus().GetEnvSource() != "exec" || synced.GetStatus().GetStanding() || synced.GetWritten() != 2 {
		t.Fatalf("SyncEnvSource(exec) = %+v", synced)
	}
	got, err := vars.GetValue(context.Background(), &envvarsv1.GetValueRequest{Tier: production, Coordinate: &envvarsv1.Coordinate{Slug: slug, Folder: "/web", Key: "API_KEY"}, Reveal: true})
	if err != nil || got.GetValue() != "k" || got.GetMetadata().GetEnvSource() != "exec" {
		t.Fatalf("GetValue() = %+v, %v", got, err)
	}
	if err := setValue(t, vars, production, cell("TOKEN"), "by-hand"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SetValue() on a cell exec owns = %v, want it refused", err)
	}
}

func TestAWritableSourceCreatesAMissingKeyThenMirrorsIt(t *testing.T) {
	vars, _ := served(t)
	fake, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret"} {
		if err := setValue(t, vars, production, cell(key), value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := syncSource(vars, production, infisicalSource(server.URL, true)); err != nil {
		t.Fatal(err)
	}

	put, err := vars.PutEnvSourceValue(context.Background(), &envvarsv1.PutEnvSourceValueRequest{
		Tier:        production,
		Coordinate:  &envvarsv1.Coordinate{Slug: slug, Folder: "/web", Key: "STRIPE_KEY"},
		Value:       "sk_live",
		Description: "The key Stripe signs with",
	})
	if err != nil {
		t.Fatalf("PutEnvSourceValue() = %v", err)
	}
	if put.GetMetadata().GetEnvSource() != "infisical:p-1/prod" || put.GetMetadata().GetVersion() != 1 {
		t.Fatalf("PutEnvSourceValue() = %+v, want the created key mirrored", put)
	}
	if len(fake.created) != 1 || fake.created[0] != "/web STRIPE_KEY The key Stripe signs with" {
		t.Fatalf("created = %v", fake.created)
	}
	_, err = vars.PutEnvSourceValue(context.Background(), &envvarsv1.PutEnvSourceValueRequest{
		Tier:       production,
		Coordinate: &envvarsv1.Coordinate{Slug: slug, Folder: "/web", Key: "STRIPE_KEY"},
		Value:      "clobber",
	})
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("PutEnvSourceValue() over a held key = %v, want AlreadyExists", err)
	}
}

func TestAReadOnlySourceTakesNoWrite(t *testing.T) {
	vars, _ := served(t)
	_, server := newInfisical(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret"} {
		if err := setValue(t, vars, production, cell(key), value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := syncSource(vars, production, infisicalSource(server.URL, false)); err != nil {
		t.Fatal(err)
	}
	_, err := vars.PutEnvSourceValue(context.Background(), &envvarsv1.PutEnvSourceValueRequest{Tier: production, Coordinate: cell("NEW"), Value: "v"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "write") {
		t.Fatalf("PutEnvSourceValue() to a read-only source = %v", err)
	}
}

func TestAnIdentityAuthOnATargetWithNoIdentityIsRefusedBeforeItIsRegistered(t *testing.T) {
	vars, _ := served(t)
	production := environmentv1.Tier_TIER_PRODUCTION
	for method, auth := range map[string]*envvarsv1.InfisicalAuth{
		"aws": {Method: &envvarsv1.InfisicalAuth_Aws{Aws: &envvarsv1.InfisicalIdentityAuth{IdentityId: "identity-1"}}},
		"gcp": {Method: &envvarsv1.InfisicalAuth_Gcp{Gcp: &envvarsv1.InfisicalIdentityAuth{IdentityId: "identity-1"}}},
	} {
		source := infisicalSource("https://infisical.example.com", false)
		source.GetInfisical().Auth = auth
		_, err := syncSource(vars, production, source)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), method) || !strings.Contains(err.Error(), "universal") {
			t.Errorf("SyncEnvSource() with %s auth on a target with no %s identity = %v, want a refusal that names universal auth as the one this target signs in with", method, method, err)
		}
		described, err := vars.DescribeEnvSource(context.Background(), &envvarsv1.DescribeEnvSourceRequest{Tier: production, Slug: slug})
		if err != nil || described.GetStatus().GetEnvSource() != "builtin" {
			t.Errorf("DescribeEnvSource() after a refused %s source = %+v, %v, want nothing registered for a syncer to fail on every poll", method, described, err)
		}
	}
}
