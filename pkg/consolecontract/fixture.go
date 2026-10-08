package consolecontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type Fixture struct {
	Name     string   `json:"name"`
	Request  Request  `json:"request"`
	Response Response `json:"response"`
	Dynamic  []string `json:"dynamic,omitempty"`
}

type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body"`
}

type Response struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

type Binding struct {
	Values map[string]string
}

var placeholder = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

func ReadFixtures(t testing.TB, dir string) []Fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("%s holds no fixtures", dir)
	}
	fixtures := make([]Fixture, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var fixture Fixture
		if err := decoder.Decode(&fixture); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if want := strings.TrimSuffix(filepath.Base(path), ".json"); fixture.Name != want {
			t.Fatalf("%s names itself %q, want %q", path, fixture.Name, want)
		}
		fixtures = append(fixtures, fixture)
	}
	return fixtures
}

func (f Fixture) Resolve(t testing.TB, b Binding) Fixture {
	t.Helper()
	resolved := f
	resolved.Request.Path = substitute(t, f.Name, f.Request.Path, b, false)
	resolved.Request.Headers = map[string]string{}
	for name, value := range f.Request.Headers {
		resolved.Request.Headers[name] = substitute(t, f.Name, value, b, false)
	}
	resolved.Request.Body = json.RawMessage(substitute(t, f.Name, string(f.Request.Body), b, true))
	resolved.Response.Body = json.RawMessage(substitute(t, f.Name, string(f.Response.Body), b, true))
	return resolved
}

func substitute(t testing.TB, name, text string, b Binding, inJSON bool) string {
	t.Helper()
	return placeholder.ReplaceAllStringFunc(text, func(match string) string {
		key := placeholder.FindStringSubmatch(match)[1]
		if value, bound := b.Values[key]; bound {
			if !inJSON {
				return value
			}
			encoded, _ := json.Marshal(value)
			return string(encoded[1 : len(encoded)-1])
		}
		t.Fatalf("fixture %s uses {{%s}}, which the binding does not hold", name, key)
		return match
	})
}

type FixtureServer struct {
	URL string
}

func ServeFixture(t testing.TB, f Fixture, b Binding) *FixtureServer {
	t.Helper()
	f = f.Resolve(t, b)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen := requests.Add(1); seen > 1 {
			t.Errorf("fixture %s: request %d arrived, want exactly one", f.Name, seen)
		}
		requireRequest(t, f, r)
		if !isNull(f.Response.Body) {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(f.Response.Status)
		if !isNull(f.Response.Body) {
			_, _ = w.Write(f.Response.Body)
		}
	}))
	t.Cleanup(func() {
		server.Close()
		if seen := requests.Load(); seen != 1 {
			t.Errorf("fixture %s: %d requests arrived, want exactly one", f.Name, seen)
		}
	})
	return &FixtureServer{URL: server.URL}
}

func requireRequest(t testing.TB, f Fixture, r *http.Request) {
	t.Helper()
	if r.Method != f.Request.Method {
		t.Errorf("fixture %s: method %s, want %s", f.Name, r.Method, f.Request.Method)
	}
	if r.RequestURI != f.Request.Path {
		t.Errorf("fixture %s: path %s, want %s", f.Name, r.RequestURI, f.Request.Path)
	}
	expected := http.Header{}
	for name, value := range f.Request.Headers {
		expected.Set(name, value)
	}
	names := []string{"Authorization", "Content-Type"}
	for name := range f.Request.Headers {
		names = append(names, http.CanonicalHeaderKey(name))
	}
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		want, listed := expected[name]
		got, sent := r.Header[name]
		switch {
		case listed != sent:
			t.Errorf("fixture %s: header %s is %q, want %q", f.Name, name, got, want)
		case listed:
			if got[0] != want[0] {
				t.Errorf("fixture %s: header %s is %q, want %q", f.Name, name, got[0], want[0])
			}
		}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("fixture %s: read request body: %v", f.Name, err)
		return
	}
	if isNull(f.Request.Body) {
		if len(body) != 0 {
			t.Errorf("fixture %s: request body %s, want none", f.Name, body)
		}
		return
	}
	var got, want any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Errorf("fixture %s: request body %q is not JSON: %v", f.Name, body, err)
		return
	}
	if err := json.Unmarshal(f.Request.Body, &want); err != nil {
		t.Errorf("fixture %s: fixture request body: %v", f.Name, err)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fixture %s: request body %s, want %s", f.Name, body, f.Request.Body)
	}
}

func RequireDecodedBody(t testing.TB, f Fixture, b Binding, decoded any) {
	t.Helper()
	f = f.Resolve(t, b)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("fixture %s: encode what the client decoded: %v", f.Name, err)
	}
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(f.Response.Body, &want); err != nil {
		t.Fatalf("fixture %s: fixture response body: %v", f.Name, err)
	}
	for _, mismatch := range decodedMismatches("$", got, want) {
		t.Errorf("fixture %s: %s", f.Name, mismatch)
	}
}

func decodedMismatches(path string, got, want any) []string {
	switch decoded := got.(type) {
	case map[string]any:
		sent, isObject := want.(map[string]any)
		if !isObject {
			return []string{fmt.Sprintf("%s decodes as an object, but the console sends %v", path, want)}
		}
		var mismatches []string
		for key, value := range decoded {
			fixture, present := sent[key]
			if !present {
				mismatches = append(mismatches, fmt.Sprintf("%s.%s is decoded, but the console never sends it", path, key))
				continue
			}
			mismatches = append(mismatches, decodedMismatches(path+"."+key, value, fixture)...)
		}
		return mismatches
	case []any:
		sent, isArray := want.([]any)
		if !isArray {
			return []string{fmt.Sprintf("%s decodes as an array, but the console sends %v", path, want)}
		}
		if len(decoded) != len(sent) {
			return []string{fmt.Sprintf("%s decodes %d elements, but the console sends %d", path, len(decoded), len(sent))}
		}
		var mismatches []string
		for i := range decoded {
			mismatches = append(mismatches, decodedMismatches(fmt.Sprintf("%s[%d]", path, i), decoded[i], sent[i])...)
		}
		return mismatches
	default:
		if !reflect.DeepEqual(got, want) {
			return []string{fmt.Sprintf("%s decodes as %v, but the console sends %v", path, got, want)}
		}
		return nil
	}
}

func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
