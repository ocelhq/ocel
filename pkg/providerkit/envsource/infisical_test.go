package envsource_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

type fakeSecret struct {
	id      string
	key     string
	value   string
	comment string
	version int
	hidden  bool
}

type fakeImport struct {
	path    string
	secrets []fakeSecret
}

type fakeInfisical struct {
	t *testing.T

	mu          sync.Mutex
	clientID    string
	secret      string
	token       string
	logins      int
	orgID       string
	folders     map[string]bool
	secrets     map[string][]fakeSecret
	imports     map[string][]fakeImport
	listed      []string
	throttle    int
	loginBodies []map[string]string
	approval    bool
}

func newFakeInfisical(t *testing.T) (*fakeInfisical, *httptest.Server) {
	fake := &fakeInfisical{
		t:        t,
		clientID: "client-id",
		secret:   "client-secret",
		orgID:    "org-1",
		folders:  map[string]bool{"/": true},
		secrets:  map[string][]fakeSecret{},
		imports:  map[string][]fakeImport{},
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return fake, server
}

func (f *fakeInfisical) put(path string, secrets ...fakeSecret) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folders[path] = true
	f.secrets[path] = append(f.secrets[path], secrets...)
}

func (f *fakeInfisical) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.throttle > 0 {
		f.throttle--
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": 429, "error": "RateLimitExceeded", "message": "Rate limit exceeded. Please try again in 0 seconds"})
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/auth/") && strings.HasSuffix(r.URL.Path, "/login") {
		f.login(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token || f.token == "" {
		f.fail(w, http.StatusUnauthorized, "UnauthorizedError", "Token missing or expired")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v4/secrets":
		f.list(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v4/secrets/"):
		f.create(w, r, strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/"))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/folders":
		f.folder(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/p-1":
		_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": "p-1", "orgId": f.orgID}})
	default:
		f.fail(w, http.StatusNotFound, "NotFound", r.Method+" "+r.URL.Path)
	}
}

func (f *fakeInfisical) fail(w http.ResponseWriter, status int, kind, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "error": kind, "message": message})
}

func (f *fakeInfisical) login(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	f.loginBodies = append(f.loginBodies, body)
	if r.URL.Path == "/api/v1/auth/universal-auth/login" && (body["clientId"] != f.clientID || body["clientSecret"] != f.secret) {
		f.fail(w, http.StatusUnauthorized, "UnauthorizedError", "Invalid credentials")
		return
	}
	f.logins++
	f.token = fmt.Sprintf("token-%d", f.logins)
	_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": f.token, "expiresIn": 3600, "accessTokenMaxTTL": 86400, "tokenType": "Bearer"})
}

func (f *fakeInfisical) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("projectId") != "p-1" || query.Get("environment") != "prod" {
		f.fail(w, http.StatusBadRequest, "BadRequest", "Missing project id or environment")
		return
	}
	path := query.Get("secretPath")
	f.listed = append(f.listed, path)
	if !f.folders[path] {
		f.fail(w, http.StatusNotFound, "SecretPathNotFound", "Folder with path '"+path+"' not found")
		return
	}
	render := func(secret fakeSecret, path string) map[string]any {
		value := secret.value
		if secret.hidden {
			value = "<hidden-by-infisical>"
		}
		return map[string]any{"id": secret.id, "_id": secret.id, "secretKey": secret.key, "secretValue": value, "secretComment": secret.comment, "version": secret.version, "secretPath": path, "type": "shared", "secretValueHidden": secret.hidden}
	}
	secrets := []map[string]any{}
	for _, secret := range f.secrets[path] {
		secrets = append(secrets, render(secret, path))
	}
	imports := []map[string]any{}
	if query.Get("includeImports") == "true" {
		for _, imported := range f.imports[path] {
			rendered := []map[string]any{}
			for _, secret := range imported.secrets {
				rendered = append(rendered, render(secret, imported.path))
			}
			imports = append(imports, map[string]any{"secretPath": imported.path, "environment": "prod", "secrets": rendered})
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"secrets": secrets, "imports": imports})
}

func (f *fakeInfisical) create(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		ProjectID   string `json:"projectId"`
		Environment string `json:"environment"`
		SecretPath  string `json:"secretPath"`
		SecretValue string `json:"secretValue"`
		Comment     string `json:"secretComment"`
		Type        string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if body.Type != "shared" || body.ProjectID != "p-1" || body.Environment != "prod" {
		f.fail(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("unexpected create %+v", body))
		return
	}
	if !f.folders[body.SecretPath] {
		f.fail(w, http.StatusNotFound, "NotFound", "Folder with path '"+body.SecretPath+"' in environment with slug 'prod' not found")
		return
	}
	for _, held := range f.secrets[body.SecretPath] {
		if held.key == name {
			f.fail(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("Secret '%s' already exists in path '%s' of environment 'prod'", name, body.SecretPath))
			return
		}
	}
	if f.approval {
		_ = json.NewEncoder(w).Encode(map[string]any{"approval": map[string]any{"id": "a-1"}})
		return
	}
	created := fakeSecret{id: "new-" + name, key: name, value: body.SecretValue, comment: body.Comment, version: 1}
	f.secrets[body.SecretPath] = append(f.secrets[body.SecretPath], created)
	_ = json.NewEncoder(w).Encode(map[string]any{"secret": map[string]any{"id": created.id, "secretKey": name, "version": 1}})
}

func (f *fakeInfisical) folder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID   string `json:"projectId"`
		Environment string `json:"environment"`
		Name        string `json:"name"`
		Path        string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if !f.folders[body.Path] {
		f.fail(w, http.StatusNotFound, "NotFound", "parent folder not found")
		return
	}
	f.folders[strings.TrimSuffix(body.Path, "/")+"/"+body.Name] = true
	_ = json.NewEncoder(w).Encode(map[string]any{"folder": map[string]any{"id": "f", "name": body.Name}})
}

func infisicalAt(server *httptest.Server, path string, write envsource.WritePolicy) envsource.InfisicalOptions {
	return envsource.InfisicalOptions{
		Project:     "p-1",
		Environment: "prod",
		Path:        path,
		Host:        server.URL,
		Write:       write,
		Auth:        envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVar: "ID", ClientSecretVar: "SECRET"},
	}.Normalized()
}

func cell(folder, key string) values.Cell { return values.Cell{Folder: folder, Key: key} }

func TestInfisicalResolvesEachFolderUnderItsPathSignedInAsTheMachineIdentity(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/acme", fakeSecret{id: "s1", key: "DATABASE_URL", value: "postgres://root", version: 3})
	fake.put("/acme/web", fakeSecret{id: "s2", key: "API_KEY", value: "web-key", version: 1})
	fake.imports["/acme/web"] = []fakeImport{{path: "/shared", secrets: []fakeSecret{
		{id: "s3", key: "API_KEY", value: "shadowed", version: 9},
		{id: "s4", key: "SENTRY_DSN", value: "https://sentry", version: 2},
	}}}

	source := envsource.NewInfisical(infisicalAt(server, "/acme", envsource.WriteNever), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	resolved, err := source.Resolve(context.Background(), []string{"", "/web"})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	want := map[values.Cell]envsource.Resolved{
		cell("", "DATABASE_URL"):   {Value: []byte("postgres://root"), Version: "s1@3"},
		cell("/web", "API_KEY"):    {Value: []byte("web-key"), Version: "s2@1"},
		cell("/web", "SENTRY_DSN"): {Value: []byte("https://sentry"), Version: "s4@2"},
	}
	if len(resolved) != len(want) {
		t.Fatalf("Resolve() = %v, want %v", resolved, want)
	}
	for at, held := range want {
		if got := resolved[at]; string(got.Value) != string(held.Value) || got.Version != held.Version {
			t.Errorf("Resolve()[%v] = %s@%s, want %s@%s", at, got.Value, got.Version, held.Value, held.Version)
		}
	}
	if source.ID() != "infisical:p-1/prod" {
		t.Errorf("ID() = %q", source.ID())
	}
	if caps := source.Capabilities(); !caps.Read || !caps.List || caps.Write || !caps.Standing {
		t.Errorf("Capabilities() = %+v, want a standing reader that may not write", caps)
	}
}

func TestInfisicalReadsAFolderItLacksAsHoldingNothing(t *testing.T) {
	_, server := newFakeInfisical(t)
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	resolved, err := source.Resolve(context.Background(), []string{"/web"})
	if err != nil || len(resolved) != 0 {
		t.Fatalf("Resolve() of a folder Infisical lacks = %v, %v, want nothing and no error", resolved, err)
	}
}

func TestInfisicalTakesAnEmptyValueAsUnset(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "PLACEHOLDER", value: "", version: 1})
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	resolved, err := source.Resolve(context.Background(), []string{""})
	if err != nil || len(resolved) != 0 {
		t.Fatalf("Resolve() = %v, %v, want an empty value read as unset", resolved, err)
	}
}

func TestInfisicalRefusesAValueItMayListButNotRead(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "API_KEY", value: "x", version: 1, hidden: true})
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	_, err := source.Resolve(context.Background(), []string{""})
	if err == nil || !strings.Contains(err.Error(), "API_KEY") || strings.Contains(err.Error(), "hidden-by-infisical") {
		t.Fatalf("Resolve() of a hidden value = %v, want a refusal naming the key", err)
	}
}

func TestInfisicalWaitsOutAThrottleAndSignsInOnceAcrossReads(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	fake.throttle = 2
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	for range 2 {
		if _, err := source.Resolve(context.Background(), []string{""}); err != nil {
			t.Fatalf("Resolve() after a throttle = %v, want it retried", err)
		}
	}
	if fake.logins != 1 {
		t.Errorf("signed in %d times across two reads, want once", fake.logins)
	}
}

func TestInfisicalSignsInAgainWhenItsTokenIsRefused(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	if _, err := source.Resolve(context.Background(), []string{""}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.token = "rotated"
	fake.mu.Unlock()
	if _, err := source.Resolve(context.Background(), []string{""}); err != nil {
		t.Fatalf("Resolve() with a revoked token = %v, want a fresh sign-in", err)
	}
	if fake.logins != 2 {
		t.Errorf("logins = %d, want 2", fake.logins)
	}
}

func TestInfisicalRefusesCredentialsItRejects(t *testing.T) {
	_, server := newFakeInfisical(t)
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.UniversalAuth("client-id", "wrong"), server.Client())
	_, err := source.Resolve(context.Background(), []string{""})
	if err == nil || !strings.Contains(err.Error(), "Invalid credentials") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("Resolve() with a rejected secret = %v, want Infisical's refusal and never the secret", err)
	}
}

func TestInfisicalCreatesAMissingKeyAndNeverOverwritesOne(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/acme", fakeSecret{id: "s1", key: "HELD", value: "kept", version: 1})
	source := envsource.NewInfisical(infisicalAt(server, "/acme", envsource.WriteMissing), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	if !source.Capabilities().Write {
		t.Fatal("a source written as missing reports it may not write")
	}
	ctx := context.Background()

	if err := source.Put(ctx, cell("/web/api", "NEW_KEY"), []byte("v"), "The key the API signs with"); err != nil {
		t.Fatalf("Put() into a folder Infisical lacks = %v, want the folders made and the key created", err)
	}
	created := fake.secrets["/acme/web/api"]
	if len(created) != 1 || created[0].key != "NEW_KEY" || created[0].value != "v" || created[0].comment != "The key the API signs with" {
		t.Fatalf("created = %+v", created)
	}
	if err := source.Put(ctx, cell("", "HELD"), []byte("clobber"), ""); !errors.Is(err, envsource.ErrExists) {
		t.Fatalf("Put() over a held key = %v, want ErrExists", err)
	}
	if fake.secrets["/acme"][0].value != "kept" {
		t.Fatal("Put() overwrote a held key")
	}

	fake.approval = true
	if err := source.Put(ctx, cell("", "GATED"), []byte("v"), ""); !errors.Is(err, envsource.ErrAwaitingApproval) {
		t.Fatalf("Put() under an approval policy = %v, want ErrAwaitingApproval", err)
	}
}

func TestInfisicalRefusesToWriteUnlessToldItMay(t *testing.T) {
	_, server := newFakeInfisical(t)
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	if err := source.Put(context.Background(), cell("", "K"), []byte("v"), ""); !errors.Is(err, envsource.ErrReadOnly) {
		t.Fatalf("Put() on a read-only source = %v, want ErrReadOnly", err)
	}
}

func TestInfisicalLinksEachCellToItsFolderInTheDashboard(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/acme/web")
	source := envsource.NewInfisical(infisicalAt(server, "/acme", envsource.WriteNever), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
	if link := source.Link(cell("/web", "API_KEY")); link != "" {
		t.Fatalf("Link() before any read = %q, want nothing until the org is known", link)
	}
	if _, err := source.Resolve(context.Background(), []string{"/web"}); err != nil {
		t.Fatal(err)
	}
	want := server.URL + "/organizations/org-1/projects/secret-management/p-1/secrets/prod?search=API_KEY&secretPath=%2Facme%2Fweb"
	if link := source.Link(cell("/web", "API_KEY")); link != want {
		t.Fatalf("Link() = %q, want %q", link, want)
	}
}

type signer struct{ calls int }

func (s *signer) SignCallerIdentity(context.Context) (envsource.SignedRequest, error) {
	s.calls++
	header := http.Header{}
	header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKID/20260925/eu-west-2/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	header.Set("X-Amz-Date", "20260925T000000Z")
	header.Set("X-Amz-Security-Token", "session")
	return envsource.SignedRequest{
		Method: http.MethodPost,
		URL:    "https://sts.eu-west-2.amazonaws.com/",
		Header: header,
		Body:   []byte("Action=GetCallerIdentity&Version=2011-06-15"),
	}, nil
}

func TestInfisicalSignsInAsAnAWSRoleWithASignedCallerIdentityRequest(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	signing := &signer{}
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.AWSAuth("identity-1", signing), server.Client())
	if _, err := source.Resolve(context.Background(), []string{""}); err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if len(fake.loginBodies) != 1 {
		t.Fatalf("logins = %v", fake.loginBodies)
	}
	body := fake.loginBodies[0]
	if body["identityId"] != "identity-1" || body["iamHttpRequestMethod"] != "POST" {
		t.Fatalf("login body = %v", body)
	}
	signedBody, err := base64.StdEncoding.DecodeString(body["iamRequestBody"])
	if err != nil || string(signedBody) != "Action=GetCallerIdentity&Version=2011-06-15" {
		t.Fatalf("iamRequestBody = %q, %v", signedBody, err)
	}
	rawHeaders, err := base64.StdEncoding.DecodeString(body["iamRequestHeaders"])
	if err != nil {
		t.Fatal(err)
	}
	var headers map[string]string
	if err := json.Unmarshal(rawHeaders, &headers); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"Host":                 "sts.eu-west-2.amazonaws.com",
		"Content-Type":         "application/x-www-form-urlencoded; charset=utf-8",
		"Content-Length":       "43",
		"X-Amz-Date":           "20260925T000000Z",
		"X-Amz-Security-Token": "session",
	} {
		if headers[name] != want {
			t.Errorf("header %s = %q, want %q", name, headers[name], want)
		}
	}
	if !strings.HasPrefix(headers["Authorization"], "AWS4-HMAC-SHA256") {
		t.Errorf("Authorization = %q", headers["Authorization"])
	}
}

type issuer struct{ audience string }

func (i *issuer) IDToken(_ context.Context, audience string) (string, error) {
	i.audience = audience
	return "jwt-for-" + audience, nil
}

func TestInfisicalSignsInAsAGCPServiceAccountWithAnIDTokenForTheIdentity(t *testing.T) {
	fake, server := newFakeInfisical(t)
	issuing := &issuer{}
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.GCPAuth("identity-2", issuing), server.Client())
	if _, err := source.Resolve(context.Background(), []string{""}); err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if issuing.audience != "identity-2" {
		t.Errorf("audience = %q, want the identity id", issuing.audience)
	}
	if body := fake.loginBodies[0]; body["identityId"] != "identity-2" || body["jwt"] != "jwt-for-identity-2" {
		t.Fatalf("login body = %v", body)
	}
}

func TestInfisicalReadsWithAnAccessTokenAsIs(t *testing.T) {
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	fake.token = "developer-token"
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.AccessToken("developer-token"), server.Client())
	resolved, err := source.Resolve(context.Background(), []string{""})
	if err != nil || string(resolved[cell("", "A")].Value) != "a" {
		t.Fatalf("Resolve() = %v, %v", resolved, err)
	}
	if fake.logins != 0 {
		t.Errorf("an access token signed in %d times, want never", fake.logins)
	}
}
