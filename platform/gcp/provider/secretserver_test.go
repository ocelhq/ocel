package gcp

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/secretmanager/v1"
)

type storedVersion struct {
	payload   string
	destroyed bool
}

type storedSecret struct {
	versions []storedVersion
	policy   *secretmanager.Policy
}

type secretServer struct {
	mu      sync.Mutex
	secrets map[string]*storedSecret
	added   int
}

func newSecretServer() *secretServer { return &secretServer{secrets: map[string]*storedSecret{}} }

func (s *secretServer) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path[strings.Index(r.URL.Path, "/secrets")+len("/secrets"):]
	path = strings.TrimPrefix(path, "/")
	switch {
	case r.Method == http.MethodPost && path == "":
		name := r.URL.Query().Get("secretId")
		if _, taken := s.secrets[name]; taken {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":{"code":409,"message":"secret already exists"}}`))
			return
		}
		s.secrets[name] = &storedSecret{policy: &secretmanager.Policy{}}
		writeBody(w, &secretmanager.Secret{Name: name})
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":addVersion"):
		secret := s.secrets[strings.TrimSuffix(path, ":addVersion")]
		asked := readBody[secretmanager.AddSecretVersionRequest](w, r)
		if secret == nil || asked == nil {
			secretAbsent(w)
			return
		}
		secret.versions = append(secret.versions, storedVersion{payload: asked.Payload.Data})
		s.added++
		writeBody(w, &secretmanager.SecretVersion{Name: strings.TrimSuffix(path, ":addVersion") + "/versions/" + strconv.Itoa(len(secret.versions))})
	case r.Method == http.MethodGet && strings.HasSuffix(path, ":access"):
		name, version, _ := strings.Cut(strings.TrimSuffix(path, ":access"), "/versions/")
		secret := s.secrets[name]
		if secret == nil || len(secret.versions) == 0 {
			secretAbsent(w)
			return
		}
		at := len(secret.versions)
		if version != "latest" {
			at, _ = strconv.Atoi(version)
		}
		if at < 1 || at > len(secret.versions) {
			secretAbsent(w)
			return
		}
		if secret.versions[at-1].destroyed {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"secret version is in DESTROYED state"}}`))
			return
		}
		writeBody(w, &secretmanager.AccessSecretVersionResponse{Name: name + "/versions/" + strconv.Itoa(at), Payload: &secretmanager.SecretPayload{Data: secret.versions[at-1].payload}})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/versions"):
		name := strings.TrimSuffix(path, "/versions")
		secret := s.secrets[name]
		if secret == nil {
			secretAbsent(w)
			return
		}
		listed := &secretmanager.ListSecretVersionsResponse{}
		for at, version := range secret.versions {
			state := "ENABLED"
			if version.destroyed {
				state = "DESTROYED"
			}
			listed.Versions = append(listed.Versions, &secretmanager.SecretVersion{Name: "projects/123/secrets/" + name + "/versions/" + strconv.Itoa(at+1), State: state})
		}
		slices.Reverse(listed.Versions)
		writeBody(w, listed)
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":destroy"):
		name, version, _ := strings.Cut(strings.TrimSuffix(path, ":destroy"), "/versions/")
		secret := s.secrets[name]
		at, _ := strconv.Atoi(version)
		if secret == nil || at < 1 || at > len(secret.versions) {
			secretAbsent(w)
			return
		}
		secret.versions[at-1].destroyed = true
		writeBody(w, &secretmanager.SecretVersion{Name: name + "/versions/" + version, State: "DESTROYED"})
	case r.Method == http.MethodGet && strings.HasSuffix(path, ":getIamPolicy"):
		secret := s.secrets[strings.TrimSuffix(path, ":getIamPolicy")]
		if secret == nil {
			secretAbsent(w)
			return
		}
		writeBody(w, secret.policy)
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":setIamPolicy"):
		secret := s.secrets[strings.TrimSuffix(path, ":setIamPolicy")]
		asked := readBody[secretmanager.SetIamPolicyRequest](w, r)
		if secret == nil || asked == nil {
			secretAbsent(w)
			return
		}
		secret.policy = asked.Policy
		writeBody(w, secret.policy)
	case r.Method == http.MethodDelete:
		if _, found := s.secrets[path]; !found {
			secretAbsent(w)
			return
		}
		delete(s.secrets, path)
		writeBody(w, struct{}{})
	default:
		t.Errorf("Secret Manager was called %s %s, which nothing here serves", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func secretAbsent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error":{"code":404,"message":"secret not found"}}`))
}

func (s *secretServer) latest(t *testing.T, name string) (string, bool) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	secret := s.secrets[name]
	if secret == nil || len(secret.versions) == 0 {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(secret.versions[len(secret.versions)-1].payload)
	if err != nil {
		t.Fatalf("secret %s holds a version that is no base64: %v", name, err)
	}
	return string(raw), true
}

func (s *secretServer) has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.secrets[name]
	return found
}

func (s *secretServer) readers(name, role string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret := s.secrets[name]
	if secret == nil {
		return nil
	}
	at := slices.IndexFunc(secret.policy.Bindings, func(binding *secretmanager.Binding) bool { return binding.Role == role })
	if at < 0 {
		return nil
	}
	return slices.Clone(secret.policy.Bindings[at].Members)
}

func (s *secretServer) versionsAdded() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.added
}

func (s *secretServer) enabledVersions(name string) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret := s.secrets[name]
	if secret == nil {
		return nil
	}
	var enabled []int
	for at, version := range secret.versions {
		if !version.destroyed {
			enabled = append(enabled, at+1)
		}
	}
	return enabled
}

func (s *secretServer) destroy(name string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[name].versions[version-1].destroyed = true
}
