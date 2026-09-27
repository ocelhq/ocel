package envsource_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
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
	unavailable int
	loginBodies []map[string]string
	loginPaths  []string
	approval    bool
	refuseList  bool
}

func newFakeInfisical(t *testing.T) (*fakeInfisical, *httptest.Server) {
	t.Helper()
	fake := &fakeInfisical{
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

func (f *fakeInfisical) stored(path string) []fakeSecret {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSecret(nil), f.secrets[path]...)
}

func (f *fakeInfisical) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fakeInfisical) listedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.listed...)
}

func (f *fakeInfisical) set(change func(*fakeInfisical)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeInfisical) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.throttle > 0 {
		f.throttle--
		w.Header().Set("Retry-After", "0")
		f.fail(w, http.StatusTooManyRequests, "RateLimitExceeded", "Rate limit exceeded. Please try again in 0 seconds")
		return
	}
	if f.unavailable > 0 {
		f.unavailable--
		f.fail(w, http.StatusServiceUnavailable, "ServiceUnavailable", "try again")
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/auth/") && strings.HasSuffix(r.URL.Path, "/login") {
		f.login(w, r)
		return
	}
	if f.token == "" || r.Header.Get("Authorization") != "Bearer "+f.token {
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
	f.loginPaths = append(f.loginPaths, r.URL.Path)
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
	if f.refuseList {
		f.fail(w, http.StatusForbidden, "PermissionDenied", "You are not allowed to read secrets")
		return
	}
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
	for _, existing := range f.secrets[body.SecretPath] {
		if existing.key == name {
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
		Auth:        envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVariable: "ID", ClientSecretVariable: "SECRET"},
	}.Normalize()
}

func cell(folder, key string) envvars.Cell { return envvars.Cell{Folder: folder, Key: key} }

func signedIn(server *httptest.Server, path string, write envsource.WritePolicy) envsource.Source {
	return envsource.NewInfisical(infisicalAt(server, path, write), envsource.UniversalAuth("client-id", "client-secret"), server.Client())
}

func TestInfisicalReadsEachFolderUnderItsPathWithImportsBeneathItsOwnValues(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/acme", fakeSecret{id: "s1", key: "DATABASE_URL", value: "postgres://root", version: 3})
	fake.put("/acme/web", fakeSecret{id: "s2", key: "API_KEY", value: "web-key", version: 1})
	fake.set(func(f *fakeInfisical) {
		f.imports["/acme/web"] = []fakeImport{{path: "/shared", secrets: []fakeSecret{
			{id: "s3", key: "API_KEY", value: "shadowed", version: 9},
			{id: "s4", key: "SENTRY_DSN", value: "https://sentry", version: 2},
		}}}
	})

	source := signedIn(server, "/acme", envsource.WriteNever)
	read, err := source.Read(context.Background(), []string{"", "/web"})
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	want := map[envvars.Cell]envsource.Value{
		cell("", "DATABASE_URL"):   {Plaintext: []byte("postgres://root"), Version: "s1@3"},
		cell("/web", "API_KEY"):    {Plaintext: []byte("web-key"), Version: "s2@1"},
		cell("/web", "SENTRY_DSN"): {Plaintext: []byte("https://sentry"), Version: "s4@2"},
	}
	if len(read) != len(want) {
		t.Fatalf("Read() = %v, want %v", read, want)
	}
	for at, value := range want {
		if got := read[at]; string(got.Plaintext) != string(value.Plaintext) || got.Version != value.Version {
			t.Errorf("Read()[%v] = %s@%s, want %s@%s", at, got.Plaintext, got.Version, value.Plaintext, value.Version)
		}
	}
	if listed := fake.listedPaths(); !slices.Equal(listed, []string{"/acme", "/acme/web"}) {
		t.Errorf("listed %v, want each folder listed once under the path", listed)
	}
	if source.ID() != "infisical:p-1/prod" {
		t.Errorf("ID() = %q", source.ID())
	}
}

func TestInfisicalReadsAFolderItLacksAsEmpty(t *testing.T) {
	t.Parallel()
	_, server := newFakeInfisical(t)
	read, err := signedIn(server, "/", envsource.WriteNever).Read(context.Background(), []string{"/web"})
	if err != nil || len(read) != 0 {
		t.Fatalf("Read() of a folder Infisical lacks = %v, %v, want nothing and no error", read, err)
	}
}

func TestInfisicalReadsAnEmptyValueAsUnset(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "PLACEHOLDER", value: "", version: 1})
	read, err := signedIn(server, "/", envsource.WriteNever).Read(context.Background(), []string{""})
	if err != nil || len(read) != 0 {
		t.Fatalf("Read() = %v, %v, want an empty value read as unset", read, err)
	}
}

func TestInfisicalRefusesAValueItMayListButNotRead(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "API_KEY", value: "x", version: 1, hidden: true})
	_, err := signedIn(server, "/", envsource.WriteNever).Read(context.Background(), []string{""})
	if err == nil || !strings.Contains(err.Error(), "API_KEY") || strings.Contains(err.Error(), "hidden-by-infisical") {
		t.Fatalf("Read() of a hidden value = %v, want a refusal naming the key", err)
	}
}

func TestInfisicalWaitsOutAThrottleAndLogsInOnceAcrossReads(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	fake.set(func(f *fakeInfisical) { f.throttle = 2 })
	source := signedIn(server, "/", envsource.WriteNever)
	for range 2 {
		if _, err := source.Read(context.Background(), []string{""}); err != nil {
			t.Fatalf("Read() after a throttle = %v, want it retried", err)
		}
	}
	if logins := fake.loginCount(); logins != 1 {
		t.Errorf("logged in %d times across two reads, want once", logins)
	}
}

func TestInfisicalRetriesAReadThroughAGatewayError(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	fake.set(func(f *fakeInfisical) { f.unavailable = 1 })
	if _, err := signedIn(server, "/", envsource.WriteNever).Read(context.Background(), []string{""}); err != nil {
		t.Fatalf("Read() through one 503 = %v, want it retried", err)
	}
}

func TestInfisicalLogsInAgainWhenItsTokenIsRefused(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	source := signedIn(server, "/", envsource.WriteNever)
	if _, err := source.Read(context.Background(), []string{""}); err != nil {
		t.Fatal(err)
	}
	fake.set(func(f *fakeInfisical) { f.token = "rotated" })
	if _, err := source.Read(context.Background(), []string{""}); err != nil {
		t.Fatalf("Read() with a revoked token = %v, want a fresh login", err)
	}
	if logins := fake.loginCount(); logins != 2 {
		t.Errorf("logins = %d, want 2", logins)
	}
}

func TestInfisicalRefusesCredentialsItRejectsWithoutRepeatingThem(t *testing.T) {
	t.Parallel()
	_, server := newFakeInfisical(t)
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.UniversalAuth("client-id", "wrong-secret"), server.Client())
	_, err := source.Read(context.Background(), []string{""})
	if err == nil || !strings.Contains(err.Error(), "Invalid credentials") || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("Read() with a rejected secret = %v, want Infisical's refusal and never the secret", err)
	}
}

func TestInfisicalCreatesAMissingKeyAndNeverOverwritesOne(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/acme", fakeSecret{id: "s1", key: "STORED", value: "kept", version: 1})
	source := signedIn(server, "/acme", envsource.WriteMissing)
	ctx := context.Background()

	if err := source.Create(ctx, cell("/web/api", "NEW_KEY"), []byte("v"), "The key the API signs with"); err != nil {
		t.Fatalf("Create() in a folder Infisical lacks = %v, want the folders made and the key created", err)
	}
	created := fake.stored("/acme/web/api")
	if len(created) != 1 || created[0].key != "NEW_KEY" || created[0].value != "v" || created[0].comment != "The key the API signs with" {
		t.Fatalf("created = %+v", created)
	}
	if err := source.Create(ctx, cell("", "STORED"), []byte("clobber"), ""); !errors.Is(err, envsource.ErrExists) {
		t.Fatalf("Create() over a stored key = %v, want ErrExists", err)
	}
	if fake.stored("/acme")[0].value != "kept" {
		t.Fatal("Create() overwrote a stored key")
	}

	fake.set(func(f *fakeInfisical) { f.approval = true })
	if err := source.Create(ctx, cell("", "GATED"), []byte("v"), ""); !errors.Is(err, envsource.ErrAwaitingApproval) {
		t.Fatalf("Create() under an approval policy = %v, want ErrAwaitingApproval", err)
	}
}

func TestInfisicalRefusesToWriteUnlessToldItMay(t *testing.T) {
	t.Parallel()
	_, server := newFakeInfisical(t)
	if err := signedIn(server, "/", envsource.WriteNever).Create(context.Background(), cell("", "K"), []byte("v"), ""); !errors.Is(err, envsource.ErrReadOnly) {
		t.Fatalf("Create() on a read-only source = %v, want ErrReadOnly", err)
	}
}

func TestInfisicalGivesEachCellTheURLOfItsFolderInTheDashboard(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/acme/web")
	source := signedIn(server, "/acme", envsource.WriteNever)
	if url := source.URL(cell("/web", "API_KEY")); url != "" {
		t.Fatalf("URL() before any read = %q, want nothing until the org is known", url)
	}
	if _, err := source.Read(context.Background(), []string{"/web"}); err != nil {
		t.Fatal(err)
	}
	want := server.URL + "/organizations/org-1/projects/secret-management/p-1/secrets/prod?search=API_KEY&secretPath=%2Facme%2Fweb"
	if url := source.URL(cell("/web", "API_KEY")); url != want {
		t.Fatalf("URL() = %q, want %q", url, want)
	}
}

func proveBySignedRequest(context.Context, string) (envsource.IdentityProof, error) {
	header := http.Header{}
	header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKID/20260925/eu-west-2/sts/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	header.Set("X-Amz-Date", "20260925T000000Z")
	header.Set("X-Amz-Security-Token", "session")
	return envsource.IdentityProof{SignedRequest: &envsource.SignedRequest{
		Method: http.MethodPost,
		URL:    "https://sts.eu-west-2.amazonaws.com/",
		Header: header,
		Body:   []byte("Action=GetCallerIdentity&Version=2011-06-15"),
	}}, nil
}

func TestAnIdentityProvedByASignedRequestLogsInWithInfisicalsAWSAuth(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.IdentityAuth("identity-1", proveBySignedRequest), server.Client())
	if _, err := source.Read(context.Background(), []string{""}); err != nil {
		t.Fatalf("Read() = %v", err)
	}
	var body map[string]string
	var path string
	fake.set(func(f *fakeInfisical) { body, path = f.loginBodies[0], f.loginPaths[0] })
	if path != "/api/v1/auth/aws-auth/login" {
		t.Fatalf("logged in at %s, want aws-auth", path)
	}
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

func TestAnIdentityProvedByAnIDTokenForTheIdentityLogsInWithInfisicalsGCPAuth(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	var audience string
	prove := func(_ context.Context, requested string) (envsource.IdentityProof, error) {
		audience = requested
		return envsource.IdentityProof{IDToken: "jwt-for-" + requested}, nil
	}
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.IdentityAuth("identity-2", prove), server.Client())
	if _, err := source.Read(context.Background(), []string{""}); err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if audience != "identity-2" {
		t.Errorf("audience = %q, want the identity id", audience)
	}
	var body map[string]string
	var path string
	fake.set(func(f *fakeInfisical) { body, path = f.loginBodies[0], f.loginPaths[0] })
	if path != "/api/v1/auth/gcp-auth/login" {
		t.Fatalf("logged in at %s, want gcp-auth", path)
	}
	if body["identityId"] != "identity-2" || body["jwt"] != "jwt-for-identity-2" {
		t.Fatalf("login body = %v", body)
	}
}

func TestAnIdentityThisTargetCannotProveIsRefusedBeforeAnyLogin(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	signed, _ := proveBySignedRequest(context.Background(), "")
	for name, prove := range map[string]func(context.Context, string) (envsource.IdentityProof, error){
		"no way to prove it": nil,
		"an empty proof": func(context.Context, string) (envsource.IdentityProof, error) {
			return envsource.IdentityProof{}, nil
		},
		"a proof of two kinds": func(context.Context, string) (envsource.IdentityProof, error) {
			return envsource.IdentityProof{SignedRequest: signed.SignedRequest, IDToken: "jwt"}, nil
		},
		"a failed proof": func(context.Context, string) (envsource.IdentityProof, error) {
			return envsource.IdentityProof{}, errors.New("no credentials")
		},
	} {
		source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.IdentityAuth("identity-1", prove), server.Client())
		if _, err := source.Read(context.Background(), []string{""}); err == nil {
			t.Errorf("Read() with %s = nil, want a refusal", name)
		}
	}
	if logins := fake.loginCount(); logins != 0 {
		t.Errorf("logged in %d times with no proof, want never", logins)
	}
}

func TestInfisicalReadsWithAnAccessTokenAsIs(t *testing.T) {
	t.Parallel()
	fake, server := newFakeInfisical(t)
	fake.put("/", fakeSecret{id: "s1", key: "A", value: "a", version: 1})
	fake.set(func(f *fakeInfisical) { f.token = "developer-token" })
	source := envsource.NewInfisical(infisicalAt(server, "/", envsource.WriteNever), envsource.AccessToken("developer-token"), server.Client())
	read, err := source.Read(context.Background(), []string{""})
	if err != nil || string(read[cell("", "A")].Plaintext) != "a" {
		t.Fatalf("Read() = %v, %v", read, err)
	}
	if logins := fake.loginCount(); logins != 0 {
		t.Errorf("an access token logged in %d times, want never", logins)
	}
}
