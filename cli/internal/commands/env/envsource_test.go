package env

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

const infisicalConfig = `
export default {
  slug: "` + clitest.FixtureSlug + `",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  envSource: {
    production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { $env: "INFISICAL_CLIENT_ID" }, clientSecret: { $env: "INFISICAL_CLIENT_SECRET" } } } } },
    preview: { exec: { command: ["sh", "-c", "printf 'LOG_LEVEL=debug'"], format: "dotenv" } },
  },
};
`

type infisicalProject struct {
	URL string

	mu       sync.Mutex
	secrets  map[string]string
	versions map[string]int
	created  []string
	updated  []string
	refusal  string
}

func serveInfisicalProject(t *testing.T, secrets map[string]string) *infisicalProject {
	t.Helper()
	project := &infisicalProject{secrets: secrets, versions: map[string]int{}}
	if project.secrets == nil {
		project.secrets = map[string]string{}
	}
	server := httptest.NewServer(project)
	t.Cleanup(server.Close)
	project.URL = server.URL
	return project
}

func (p *infisicalProject) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/")
	switch {
	case r.URL.Path == "/api/v1/auth/universal-auth/login":
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expiresIn": 3600})
	case r.Header.Get("Authorization") != "Bearer token":
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/api/v1/projects/p-1":
		_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"orgId": "org-1"}})
	case p.refusal != "":
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": p.refusal})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v4/secrets":
		secrets := []map[string]any{}
		for key, value := range p.secrets {
			secrets = append(secrets, map[string]any{"id": key, "secretKey": key, "secretValue": value, "version": p.versions[key] + 1})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": secrets})
	case r.Method == http.MethodGet:
		if _, exists := p.secrets[key]; !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secret": map[string]any{"id": key, "secretKey": key, "version": p.versions[key] + 1}})
	case r.Method == http.MethodPost:
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, exists := p.secrets[key]; exists {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Secret '" + key + "' already exists"})
			return
		}
		p.secrets[key] = body["secretValue"]
		p.created = append(p.created, key+"="+body["secretValue"])
		_ = json.NewEncoder(w).Encode(map[string]any{"secret": map[string]any{"id": key}})
	case r.Method == http.MethodPatch:
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, exists := p.secrets[key]; !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		p.secrets[key] = body["secretValue"]
		p.versions[key]++
		p.updated = append(p.updated, key+"="+body["secretValue"])
		_ = json.NewEncoder(w).Encode(map[string]any{"secret": map[string]any{"id": key}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *infisicalProject) refuseReads(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refusal = message
}

func (p *infisicalProject) writes() (created, updated []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.created), slices.Clone(p.updated)
}

func (p *infisicalProject) at(write envsource.WritePolicy) envsource.Descriptor {
	options, err := json.Marshal(envsource.InfisicalOptions{
		Project: "p-1", Environment: "prod", Path: "/", Host: p.URL, Write: write,
		Auth: &envsource.InfisicalAuth{Universal: &envsource.UniversalAuth{
			ClientID:     envsource.Variable{Name: "INFISICAL_CLIENT_ID"},
			ClientSecret: envsource.Variable{Name: "INFISICAL_CLIENT_SECRET"},
		}},
	})
	if err != nil {
		panic(err)
	}
	descriptor, err := envsource.NewDescriptor("infisical", options)
	if err != nil {
		panic(err)
	}
	return descriptor
}

func setUpEnvSourceFixture(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := setUpEnvFixture(t)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), infisicalConfig)
	return project
}

func registerEnvSource(t *testing.T, project clitest.FakeProject, tier environment.Tier, descriptor envsource.Descriptor) {
	t.Helper()
	registration := envsource.Registration{Project: clitest.FixtureSlug, Descriptor: descriptor, Folders: []string{""}}
	if _, err := envsource.Register(context.Background(), valuesOf(project), tier, registration); err != nil {
		t.Fatalf("register %s for %s: %v", descriptor.ID(), tier, err)
	}
}

func setCredentials(t *testing.T, root string) {
	t.Helper()
	envSet(t, root, "INFISICAL_CLIENT_ID", "client-id", envOptions{})
	envSet(t, root, "INFISICAL_CLIENT_SECRET", "client-secret", envOptions{})
}

func envSync(t *testing.T, root string) string {
	t.Helper()
	var synced bytes.Buffer
	if err := runEnvSync(context.Background(), newTestDependencies(), root, envOptions{}, &synced, &synced); err != nil {
		t.Fatalf("runEnvSync err = %v; out=%s", err, synced.String())
	}
	return synced.String()
}

func syncedEnvSourceFixture(t *testing.T, write envsource.WritePolicy) (clitest.FakeProject, *infisicalProject) {
	t.Helper()
	project := setUpEnvSourceFixture(t)
	source := serveInfisicalProject(t, map[string]string{"STRIPE_API_KEY": "sk", "RETIRED": "old"})
	setCredentials(t, project.Root)
	registerEnvSource(t, project, environment.TierProduction, source.at(write))
	envSync(t, project.Root)
	return project, source
}

func TestEnvSetTakesTheCredentialsATiersEnvSourceLogsInWith(t *testing.T) {
	project := setUpEnvSourceFixture(t)
	root := project.Root
	if out := envSet(t, root, "INFISICAL_CLIENT_SECRET", "secret", envOptions{}); !strings.Contains(out, "INFISICAL_CLIENT_SECRET") {
		t.Errorf("set stdout = %q, want the credential set though no app declares it", out)
	}

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "INFISICAL_CLIENT_SECRET", "secret", envOptions{folder: "/web"}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(stderr.String(), "infisical:p-1/prod") {
		t.Fatalf("runEnvSet --folder err = %v, want a credential in a folder refused, naming the env source", err)
	}

	clitest.Bootstrap(t, project.Provider, environment.TierPreview)
	stderr.Reset()
	err = runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "INFISICAL_CLIENT_SECRET", "secret", envOptions{preview: true}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(stderr.String(), "declares") {
		t.Fatalf("runEnvSet --preview err = %v, want preview's exec source to have no credential to take", err)
	}
}

func TestEnvSyncReReadsTheEnvSourceADeployRegistered(t *testing.T) {
	t.Run("a tier nothing registered has nothing to sync, and says a deploy reads what the config names", func(t *testing.T) {
		root := setUpEnvSourceFixture(t).Root

		var stdout, stderr bytes.Buffer
		if err := runEnvSync(context.Background(), newStreamedDependencies(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSync err = %v; stderr=%s", err, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"builtin", "nothing to sync", "infisical:p-1/prod", "ocel deploy"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		}
	})

	t.Run("a registered source is read now, and ls names it as each value's source", func(t *testing.T) {
		project := setUpEnvSourceFixture(t)
		root := project.Root
		source := serveInfisicalProject(t, map[string]string{"STRIPE_API_KEY": "sk", "API_TOKEN": "t"})
		setCredentials(t, root)
		envSet(t, root, "LOG_LEVEL", "debug", envOptions{})
		registerEnvSource(t, project, environment.TierProduction, source.at(envsource.WriteNever))

		var stdout, stderr bytes.Buffer
		if err := runEnvSync(context.Background(), newStreamedDependencies(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSync err = %v; stderr=%s", err, stderr.String())
		}
		if out := stdout.String(); !strings.Contains(out, "infisical:p-1/prod") || !strings.Contains(out, "2 written") {
			t.Errorf("stdout = %q, want the env source named and both values written", out)
		}

		var ls bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&ls), root, envOptions{}, &ls, &ls); err != nil {
			t.Fatalf("runEnvList err = %v; out=%s", err, ls.String())
		}
		sources := map[string]string{}
		for _, line := range strings.Split(ls.String(), "\n") {
			if fields := strings.Fields(line); len(fields) > 1 {
				sources[fields[0]] = fields[len(fields)-1]
			}
		}
		for key, want := range map[string]string{"STRIPE_API_KEY": "infisical:p-1/prod", "INFISICAL_CLIENT_ID": "builtin"} {
			if sources[key] != want {
				t.Errorf("SOURCE of %s = %q, want %q; ls:\n%s", key, sources[key], want, ls.String())
			}
		}
		if _, listed := sources["LOG_LEVEL"]; listed {
			t.Errorf("LOG_LEVEL, set in ocel's own store before the tier read from an env source that lacks it, is still listed:\n%s", ls.String())
		}
	})

	t.Run("a registered exec source is re-read only by a deploy", func(t *testing.T) {
		project := setUpEnvSourceFixture(t)
		clitest.Bootstrap(t, project.Provider, environment.TierPreview)
		registerEnvSource(t, project, environment.TierPreview, execDescriptor(t))

		var stdout, stderr bytes.Buffer
		err := runEnvSync(context.Background(), newStreamedDependencies(&stderr), project.Root, envOptions{preview: true}, &stdout, &stderr)
		if err == nil || !strings.Contains(stderr.String(), "deploy") {
			t.Fatalf("runEnvSync --preview err = %v, want exec's re-read left to a deploy", err)
		}
	})
}

func TestEnvSourceDescribesWhereATierReadsFrom(t *testing.T) {
	project := setUpEnvSourceFixture(t)
	root := project.Root
	source := serveInfisicalProject(t, map[string]string{"STRIPE_API_KEY": "sk"})

	var before bytes.Buffer
	if err := runEnvSource(context.Background(), newStreamedDependencies(&before), root, envOptions{}, &before, &before); err != nil {
		t.Fatalf("runEnvSource err = %v; out=%s", err, before.String())
	}
	if out := before.String(); !strings.Contains(out, "production reads from builtin") || !strings.Contains(out, "infisical:p-1/prod") {
		t.Errorf("stdout = %q, want builtin named beside the env source the config names", out)
	}
	if out := before.String(); !strings.Contains(out, "dev reads from dotenv, then .env.local on top") {
		t.Errorf("stdout = %q, want the dev tier's env source named with .env.local over it", out)
	}

	setCredentials(t, root)
	registerEnvSource(t, project, environment.TierProduction, source.at(envsource.WriteNever))
	envSync(t, root)
	source.refuseReads("the identity lost read access")
	var refused bytes.Buffer
	if err := runEnvSync(context.Background(), newTestDependencies(), root, envOptions{}, &refused, &refused); err == nil {
		t.Fatalf("runEnvSync against an env source that refuses reads err = nil; out=%s", refused.String())
	}

	var stdout, stderr bytes.Buffer
	if err := runEnvSource(context.Background(), newStreamedDependencies(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSource err = %v; stderr=%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"production reads from infisical:p-1/prod", "synced every minute", "the identity lost read access", source.URL + "/organizations/org-1/", "INFISICAL_CLIENT_SECRET"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "configured") {
		t.Errorf("stdout = %q, want no note when the registered env source is the configured one", out)
	}
}

func TestEnvSourceSaysWhatOcelMayWriteIntoIt(t *testing.T) {
	project := setUpEnvSourceFixture(t)
	source := serveInfisicalProject(t, nil)
	for write, want := range map[envsource.WritePolicy]string{
		envsource.WriteValues:  "create or update a value there, never delete one",
		envsource.WriteMissing: "created there, never overwritten",
		envsource.WriteNever:   "",
	} {
		registerEnvSource(t, project, environment.TierProduction, source.at(write))
		var stdout, stderr bytes.Buffer
		if err := runEnvSource(context.Background(), newStreamedDependencies(&stderr), project.Root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSource err = %v; stderr=%s", err, stderr.String())
		}
		if writes := strings.Contains(stdout.String(), "writes"); writes != (want != "") || !strings.Contains(stdout.String(), want) {
			t.Errorf("write %q: stdout = %q, want %q", write, stdout.String(), want)
		}
	}
}

func TestEnvSetOnAValueTheEnvSourceOwnsSaysWhereToChangeIt(t *testing.T) {
	project, _ := syncedEnvSourceFixture(t, envsource.WriteNever)

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), project.Root, "STRIPE_API_KEY", "by-hand", envOptions{}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(stderr.String(), "infisical:p-1/prod") {
		t.Fatalf("runEnvSet err = %v, want the env source that owns the value named", err)
	}
	envSet(t, project.Root, "INFISICAL_CLIENT_SECRET", "rotated", envOptions{})
}

func TestEnvSetUpdatesAValueTheEnvSourceOwnsWhenItsWritePolicyIsValues(t *testing.T) {
	project, source := syncedEnvSourceFixture(t, envsource.WriteValues)

	out := envSet(t, project.Root, "STRIPE_API_KEY", "sk_rotated", envOptions{})
	if !strings.Contains(out, "infisical:p-1/prod") {
		t.Errorf("stdout = %q, want the env source it was written to named", out)
	}
	if _, updated := source.writes(); !slices.Contains(updated, "STRIPE_API_KEY=sk_rotated") {
		t.Fatalf("updated = %q, want STRIPE_API_KEY written through to the env source", updated)
	}
	if got := envGet(t, project.Root, "STRIPE_API_KEY", envOptions{reveal: true, yes: true}); !strings.Contains(got, "sk_rotated") {
		t.Errorf("ocel env get = %q, want the value synced straight back", got)
	}
}

func TestEnvSetCreatesAValueTheEnvSourceLacksWhenItsWritePolicyIsMissing(t *testing.T) {
	project, source := syncedEnvSourceFixture(t, envsource.WriteMissing)

	envSet(t, project.Root, "API_TOKEN", "tok", envOptions{})
	if created, _ := source.writes(); !slices.Contains(created, "API_TOKEN=tok") {
		t.Fatalf("created = %q, want API_TOKEN created in the env source", created)
	}

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), project.Root, "STRIPE_API_KEY", "sk_rotated", envOptions{}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(stderr.String(), `"values"`) {
		t.Fatalf("runEnvSet over a value the env source holds = %v, want it refused naming write \"values\"", err)
	}
}

func TestEnvSetKeepsACredentialInOcelsOwnStore(t *testing.T) {
	project, source := syncedEnvSourceFixture(t, envsource.WriteValues)

	envSet(t, project.Root, "INFISICAL_CLIENT_SECRET", "rotated", envOptions{})
	if created, updated := source.writes(); len(created)+len(updated) != 0 {
		t.Fatalf("created %q and updated %q, want a credential never written to the env source", created, updated)
	}
}

func TestTheSourceColumnNamesWhereEachValueComesFrom(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	renderValues(&stdout, []*variablestorev1.ValueMetadata{
		{Coordinate: &variablestorev1.Coordinate{Key: "OWN"}},
		{Coordinate: &variablestorev1.Coordinate{Key: "COPIED"}, EnvSource: "infisical:p-1/prod"},
		{Coordinate: &variablestorev1.Coordinate{Key: "SHARED"}, Target: &variablestorev1.Coordinate{Slug: "platform", Key: "SHARED"}},
	}, nil, nil, nil)

	sources := map[string]string{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if fields := strings.Fields(line); len(fields) > 1 {
			sources[fields[0]] = fields[len(fields)-1]
		}
	}
	for key, want := range map[string]string{"OWN": "builtin", "COPIED": "infisical:p-1/prod", "SHARED": "platform/SHARED"} {
		if sources[key] != want {
			t.Errorf("SOURCE of %s = %q, want %q; ls:\n%s", key, sources[key], want, stdout.String())
		}
	}
}

func execDescriptor(t *testing.T) envsource.Descriptor {
	t.Helper()
	descriptor, err := envsource.NewDescriptor("exec", []byte(`{"command":["sh"],"format":"json"}`))
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestEnvSourceAsJSONDescribesWhereATierReadsFromAndHowItsLastSyncWent(t *testing.T) {
	project := setUpEnvSourceFixture(t)
	root := project.Root
	source := serveInfisicalProject(t, map[string]string{"STRIPE_API_KEY": "sk"})
	setCredentials(t, root)
	registerEnvSource(t, project, environment.TierProduction, source.at(envsource.WriteValues))
	envSync(t, root)

	var stdout, stderr bytes.Buffer
	if err := runEnvSource(context.Background(), newJSONDependencies(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSource err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.DecodeResult(t, stdout.String())
	for field, want := range map[string]any{
		"tier":                "TIER_PRODUCTION",
		"envSource":           "infisical:p-1/prod",
		"ownsValues":          true,
		"scheduled":           true,
		"canUpdate":           true,
		"canCreate":           true,
		"lastError":           "",
		"configuredEnvSource": "infisical:p-1/prod",
		"devEnvSource":        "dotenv",
		"devLocalFile":        ".env.local",
	} {
		if got[field] != want {
			t.Errorf("source json %s = %v, want %v", field, got[field], want)
		}
	}
	if synced, _ := got["lastSuccessAt"].(string); synced == "" {
		t.Errorf("source json = %v, want the time of the last sync", got)
	}
	links, _ := got["links"].([]any)
	if len(links) == 0 {
		t.Fatalf("source json links = %v, want the env source's links", got["links"])
	}
	if link, _ := links[0].(map[string]any); link["folder"] != "" || !strings.HasPrefix(link["url"].(string), source.URL) {
		t.Errorf("source json link = %v, want the root folder's URL as fields", links[0])
	}
	if credentials, _ := got["credentials"].([]any); !slices.Contains(credentials, any("INFISICAL_CLIENT_SECRET")) {
		t.Errorf("source json credentials = %v, want the key its login is stored under", got["credentials"])
	}
	if strings.Contains(stdout.String(), "client-secret") {
		t.Errorf("stdout = %q, want the credential's name and never its value", stdout.String())
	}
	if len(clitest.RunEvents(t, stderr.String())) == 0 {
		t.Errorf("stream = %q, want the run's events there", stderr.String())
	}
}

func TestEnvSourceAsJSONNamesTheEnvSourceTheConfigNamesInstead(t *testing.T) {
	project := setUpEnvSourceFixture(t)

	var stdout, stderr bytes.Buffer
	if err := runEnvSource(context.Background(), newJSONDependencies(&stderr), project.Root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSource err = %v; stderr=%s", err, stderr.String())
	}
	got := clitest.DecodeResult(t, stdout.String())
	if got["envSource"] != "builtin" || got["ownsValues"] != false || got["configuredEnvSource"] != "infisical:p-1/prod" {
		t.Errorf("source json = %v, want builtin read while the config names infisical", got)
	}
}
