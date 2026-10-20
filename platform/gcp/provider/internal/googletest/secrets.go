package googletest

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

type Secrets struct {
	Mu      sync.Mutex
	Secrets map[string]*storedSecret
	added   int

	WrittenFirstElsewhere map[string]string
	Forbidden             []string

	TierTags      *TierTags
	InlineTags    map[string]map[string]string
	DeletedTagged []string
}

const ProjectNumber = "123456789"

func SecretTagParent(name string) string {
	return "//secretmanager.googleapis.com/projects/" + ProjectNumber + "/secrets/" + name
}

func NewSecrets() *Secrets { return &Secrets{Secrets: map[string]*storedSecret{}} }

func (s *Secrets) Serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	s.Mu.Lock()
	defer s.Mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path[strings.Index(r.URL.Path, "/secrets")+len("/secrets"):]
	path = strings.TrimPrefix(path, "/")
	switch {
	case r.Method == http.MethodPost && path == "":
		name := r.URL.Query().Get("secretId")
		if payload, raced := s.WrittenFirstElsewhere[name]; raced {
			delete(s.WrittenFirstElsewhere, name)
			s.Secrets[name] = &storedSecret{versions: []storedVersion{{payload: base64.StdEncoding.EncodeToString([]byte(payload))}}}
		}
		if slices.Contains(s.Forbidden, name) {
			secretForbidden(w, "secretmanager.secrets.create")
			return
		}
		if _, taken := s.Secrets[name]; taken {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":409,"message":"secret already exists"}}`))
			return
		}
		asked := ReadBody[secretmanager.Secret](w, r)
		if asked == nil {
			return
		}
		s.Secrets[name] = &storedSecret{}
		if len(asked.Tags) > 0 {
			if s.InlineTags == nil {
				s.InlineTags = map[string]map[string]string{}
			}
			s.InlineTags[name] = asked.Tags
			if s.TierTags != nil {
				s.TierTags.BindInline(SecretTagParent(name), asked.Tags)
			}
		}
		WriteBody(w, &secretmanager.Secret{Name: name})
	case r.Method == http.MethodGet && strings.HasSuffix(path, ":getIamPolicy"):
		secret := s.Secrets[strings.TrimSuffix(path, ":getIamPolicy")]
		if secret == nil {
			secretAbsent(w)
			return
		}
		policy := secret.policy
		if policy == nil {
			policy = &secretmanager.Policy{}
		}
		WriteBody(w, policy)
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":setIamPolicy"):
		secret := s.Secrets[strings.TrimSuffix(path, ":setIamPolicy")]
		asked := ReadBody[secretmanager.SetIamPolicyRequest](w, r)
		if secret == nil || asked == nil {
			secretAbsent(w)
			return
		}
		secret.policy = asked.Policy
		WriteBody(w, asked.Policy)
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":addVersion"):
		if slices.Contains(s.Forbidden, strings.TrimSuffix(path, ":addVersion")) {
			secretForbidden(w, "secretmanager.versions.add")
			return
		}
		secret := s.Secrets[strings.TrimSuffix(path, ":addVersion")]
		asked := ReadBody[secretmanager.AddSecretVersionRequest](w, r)
		if secret == nil || asked == nil {
			secretAbsent(w)
			return
		}
		secret.versions = append(secret.versions, storedVersion{payload: asked.Payload.Data})
		s.added++
		WriteBody(w, &secretmanager.SecretVersion{Name: strings.TrimSuffix(path, ":addVersion") + "/versions/" + strconv.Itoa(len(secret.versions))})
	case r.Method == http.MethodGet && strings.HasSuffix(path, ":access"):
		name, version, _ := strings.Cut(strings.TrimSuffix(path, ":access"), "/versions/")
		secret := s.Secrets[name]
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
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"secret version is in DESTROYED state"}}`))
			return
		}
		WriteBody(w, &secretmanager.AccessSecretVersionResponse{Name: name + "/versions/" + strconv.Itoa(at), Payload: &secretmanager.SecretPayload{Data: secret.versions[at-1].payload}})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/versions"):
		name := strings.TrimSuffix(path, "/versions")
		secret := s.Secrets[name]
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
		WriteBody(w, listed)
	case r.Method == http.MethodPost && strings.HasSuffix(path, ":destroy"):
		name, version, _ := strings.Cut(strings.TrimSuffix(path, ":destroy"), "/versions/")
		secret := s.Secrets[name]
		at, _ := strconv.Atoi(version)
		if secret == nil || at < 1 || at > len(secret.versions) {
			secretAbsent(w)
			return
		}
		secret.versions[at-1].destroyed = true
		WriteBody(w, &secretmanager.SecretVersion{Name: name + "/versions/" + version, State: "DESTROYED"})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/versions/latest"):
		secret := s.Secrets[strings.TrimSuffix(path, "/versions/latest")]
		if secret == nil || len(secret.versions) == 0 {
			secretAbsent(w)
			return
		}
		state := "ENABLED"
		if secret.versions[len(secret.versions)-1].destroyed {
			state = "DESTROYED"
		}
		WriteBody(w, &secretmanager.SecretVersion{Name: path, State: state})
	case r.Method == http.MethodGet && !strings.Contains(path, "/") && !strings.Contains(path, ":"):
		if _, found := s.Secrets[path]; !found {
			secretAbsent(w)
			return
		}
		WriteBody(w, &secretmanager.Secret{Name: "projects/" + ProjectNumber + "/secrets/" + path})
	case r.Method == http.MethodDelete:
		if _, found := s.Secrets[path]; !found {
			secretAbsent(w)
			return
		}
		if s.TierTags != nil && len(s.TierTags.Bound(SecretTagParent(path))) > 0 {
			s.DeletedTagged = append(s.DeletedTagged, path)
		}
		delete(s.Secrets, path)
		WriteBody(w, struct{}{})
	default:
		t.Errorf("Secret Manager was called %s %s, which nothing here serves", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func secretForbidden(w http.ResponseWriter, permission string) {
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Permission '` + permission + `' denied"}}`))
}

func secretAbsent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":404,"message":"secret not found"}}`))
}

func (s *Secrets) Latest(t *testing.T, name string) (string, bool) {
	t.Helper()
	s.Mu.Lock()
	defer s.Mu.Unlock()
	secret := s.Secrets[name]
	if secret == nil || len(secret.versions) == 0 {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(secret.versions[len(secret.versions)-1].payload)
	if err != nil {
		t.Fatalf("secret %s holds a version that is no base64: %v", name, err)
	}
	return string(raw), true
}

func (s *Secrets) Payload(t *testing.T, name, version string) (string, bool) {
	t.Helper()
	s.Mu.Lock()
	defer s.Mu.Unlock()
	secret := s.Secrets[name]
	at, err := strconv.Atoi(version)
	if secret == nil || err != nil || at < 1 || at > len(secret.versions) || secret.versions[at-1].destroyed {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(secret.versions[at-1].payload)
	if err != nil {
		t.Fatalf("version %s of secret %s is no base64: %v", version, name, err)
	}
	return string(raw), true
}

func (s *Secrets) Has(name string) bool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	_, found := s.Secrets[name]
	return found
}

func (s *Secrets) VersionsAdded() int {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.added
}

func (s *Secrets) EnabledVersions(name string) []int {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	secret := s.Secrets[name]
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

func (s *Secrets) Destroy(name string, version int) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Secrets[name].versions[version-1].destroyed = true
}

func (s *Secrets) Members(name, role string) []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	secret := s.Secrets[name]
	if secret == nil || secret.policy == nil {
		return nil
	}
	var members []string
	for _, binding := range secret.policy.Bindings {
		if binding.Role == role {
			members = append(members, binding.Members...)
		}
	}
	return members
}
