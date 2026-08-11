package varsui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

type fakeStore struct {
	cells     map[envgate.Cell]string
	versions  map[envgate.Cell]int64
	held      map[envgate.Address]string
	overrides []envgate.Stored

	environments []string
	expected     []*int64
	deletes      int
}

func newFakeStore() *fakeStore {
	return &fakeStore{cells: map[envgate.Cell]string{}, versions: map[envgate.Cell]int64{}, held: map[envgate.Address]string{}}
}

func (s *fakeStore) override(cell envgate.Cell, environment string) {
	s.overrides = append(s.overrides, envgate.Stored{Address: envgate.Address{Cell: cell, Environment: environment}, Version: 1})
	s.held[envgate.Address{Cell: cell, Environment: environment}] = "override"
}

func (s *fakeStore) List(context.Context) ([]envgate.Stored, error) {
	out := make([]envgate.Stored, 0, len(s.cells)+len(s.overrides))
	for cell := range s.cells {
		out = append(out, envgate.Stored{Address: envgate.Address{Cell: cell}, Version: s.versions[cell]})
	}
	return append(out, s.overrides...), nil
}

func (s *fakeStore) Reveal(_ context.Context, rows []envgate.Address) (map[envgate.Cell]string, error) {
	found := map[envgate.Cell]string{}
	for _, row := range rows {
		if value, ok := s.cells[row.Cell]; ok {
			found[row.Cell] = value
		}
	}
	return found, nil
}

func (s *fakeStore) version(at envgate.Address) int64 {
	if at.Environment == "" {
		return s.versions[at.Cell]
	}
	for _, row := range s.overrides {
		if row.Cell == at.Cell && row.Environment == at.Environment {
			return row.Version
		}
	}
	return 0
}

func (s *fakeStore) stale(at envgate.Address, expected *int64) bool {
	return expected != nil && *expected != s.version(at)
}

func (s *fakeStore) Set(_ context.Context, at envgate.Address, value string, expected *int64) error {
	s.expected = append(s.expected, expected)
	if s.stale(at, expected) {
		return varsui.ErrStaleValue
	}
	s.held[at] = value
	if at.Environment != "" {
		s.overrides = append(s.overrides, envgate.Stored{Address: at, Version: 1})
		return nil
	}
	s.cells[at.Cell] = value
	return nil
}

func (s *fakeStore) Delete(_ context.Context, at envgate.Address, expected *int64) error {
	s.expected = append(s.expected, expected)
	if s.stale(at, expected) {
		return varsui.ErrStaleValue
	}
	s.deletes++
	delete(s.held, at)
	if at.Environment != "" {
		s.overrides = slices.DeleteFunc(s.overrides, func(row envgate.Stored) bool {
			return row.Cell == at.Cell && row.Environment == at.Environment
		})
		return nil
	}
	delete(s.cells, at.Cell)
	delete(s.versions, at.Cell)
	return nil
}

func (s *fakeStore) History(_ context.Context, at envgate.Address) ([]varsui.Version, error) {
	_, held := s.held[at]
	if _, classWide := s.cells[at.Cell]; !held && !(classWide && at.Environment == "") {
		return nil, nil
	}
	return []varsui.Version{{Version: 2, CreatedAt: 200}, {Version: 1, CreatedAt: 100}}, nil
}

func def(key string, folders ...string) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{
		Key:      key,
		Class:    resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		Required: true,
		Folders:  folders,
	}
}

func discovered(t *testing.T, store *fakeStore, definitions ...*resourcesv1.VariableDefinition) *envgate.Gate {
	t.Helper()
	gate := envgate.New(store, envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}, {Name: "api"}}})
	if err := gate.Prefetch(context.Background()); err != nil {
		t.Fatalf("Prefetch: %v", err)
	}
	if _, err := gate.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: definitions}); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}
	return gate
}

func session(t *testing.T, store *fakeStore, definitions ...*resourcesv1.VariableDefinition) *varsui.Session {
	t.Helper()
	return serve(t, store, discovered(t, store, definitions...))
}

func serve(t *testing.T, store *fakeStore, gate *envgate.Gate) *varsui.Session {
	t.Helper()
	return serveUnder(t, context.Background(), store, gate)
}

func serveUnder(t *testing.T, ctx context.Context, store *fakeStore, gate *envgate.Gate) *varsui.Session {
	t.Helper()
	s, err := varsui.Serve(ctx, varsui.Options{
		Assets:       fstest.MapFS{"index.html": {Data: []byte("<title>vars</title>")}},
		Gate:         gate,
		Store:        store,
		Slug:         "shop",
		Environments: store.environments,
	})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func origin(s *varsui.Session) string {
	return strings.SplitN(s.URL, "#", 2)[0]
}

func request(t *testing.T, s *varsui.Session, method, path string, body any) *http.Response {
	t.Helper()
	return do(t, newRequest(t, s, method, path, body))
}

func newRequest(t *testing.T, s *varsui.Session, method, path string, body any) *http.Request {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, origin(s)+path, payload)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Origin", strings.TrimSuffix(origin(s), "/"))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	return req
}

func do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func state(t *testing.T, s *varsui.Session) varsui.State {
	t.Helper()
	resp := request(t, s, http.MethodGet, "/api/state", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/state = %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	var out varsui.State
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return out
}

func cellState(t *testing.T, s varsui.State, key, folder string) envgate.MatrixCell {
	t.Helper()
	for _, row := range s.Matrix.Rows {
		if row.Key != key {
			continue
		}
		for _, cell := range row.Cells {
			if cell.Folder == folder {
				return cell
			}
		}
	}
	t.Fatalf("state has no cell for %q in %q", key, folder)
	return envgate.MatrixCell{}
}

func format(expected []*int64) []string {
	out := make([]string, 0, len(expected))
	for _, e := range expected {
		if e == nil {
			out = append(out, "none")
			continue
		}
		out = append(out, strconv.FormatInt(*e, 10))
	}
	return out
}

func awaitStopped(t *testing.T, s *varsui.Session) {
	t.Helper()
	address := strings.TrimPrefix(origin(s), "http://")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the session at %s still accepts connections, so it never closed", address)
}

func mustGet(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

func TestServe(t *testing.T) {
	t.Parallel()

	t.Run("binds loopback only", func(t *testing.T) {
		t.Parallel()
		s := session(t, newFakeStore(), def("API_URL"))

		if !strings.HasPrefix(s.URL, "http://127.0.0.1:") {
			t.Errorf("session URL = %q, want a loopback address — a store-writing server must not be reachable off this host", s.URL)
		}
	})

	t.Run("the launch URL carries the token in the fragment", func(t *testing.T) {
		t.Parallel()
		s := session(t, newFakeStore(), def("API_URL"))

		if !strings.HasSuffix(s.URL, "#t="+s.Token) {
			t.Errorf("session URL = %q, want the token in the fragment so it never travels in a request line or a Referer", s.URL)
		}
		if len(s.Token) < 32 {
			t.Errorf("token = %q (%d chars), want one long enough not to be guessed", s.Token, len(s.Token))
		}
	})

	t.Run("serves the embedded page without a token", func(t *testing.T) {
		t.Parallel()
		s := session(t, newFakeStore(), def("API_URL"))

		resp := do(t, mustGet(t, s.URL))
		if resp.StatusCode != http.StatusOK || !strings.Contains(bodyOf(t, resp), "<title>vars</title>") {
			t.Errorf("GET / = %d, want the embedded page — it holds no secrets and must load before the token is read", resp.StatusCode)
		}
	})
}

func TestAuthorization(t *testing.T) {
	t.Parallel()

	const write = `{"key":"API_URL","folder":"","value":"x"}`

	for _, tc := range []struct {
		name    string
		label   string
		method  string
		path    string
		body    string
		prepare func(req *http.Request, s *varsui.Session)
	}{
		{
			name:   "a request without the token is refused",
			label:  "untokened PUT",
			method: http.MethodPut,
			path:   "/api/value",
			body:   write,
		},
		{
			name:   "a request with the wrong token is refused",
			label:  "wrong-token GET",
			method: http.MethodGet,
			path:   "/api/state",
			prepare: func(req *http.Request, s *varsui.Session) {
				req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", len(s.Token)))
			},
		},
		{
			name:   "a request from another origin is refused",
			label:  "cross-origin PUT",
			method: http.MethodPut,
			path:   "/api/value",
			body:   write,
			prepare: func(req *http.Request, s *varsui.Session) {
				req.Header.Set("Origin", "https://evil.example")
				req.Header.Set("Authorization", "Bearer "+s.Token)
			},
		},
		{
			name:   "a request under another hostname is refused",
			label:  "rebound GET",
			method: http.MethodGet,
			path:   "/api/state",
			prepare: func(req *http.Request, s *varsui.Session) {
				req.Host = "rebound.example"
				req.Header.Set("Authorization", "Bearer "+s.Token)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			s := session(t, store, def("API_URL"))

			var payload io.Reader
			if tc.body != "" {
				payload = strings.NewReader(tc.body)
			}
			req, err := http.NewRequest(tc.method, origin(s)+tc.path, payload)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if tc.prepare != nil {
				tc.prepare(req, s)
			}
			resp := do(t, req)

			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s = %d, want %d", tc.label, resp.StatusCode, http.StatusForbidden)
			}
			if len(store.cells) != 0 {
				t.Errorf("store holds %v, want nothing — a refused request must not have written", store.cells)
			}
		})
	}
}

func TestGetState(t *testing.T) {
	t.Parallel()

	t.Run("reads the matrix with a token", func(t *testing.T) {
		t.Parallel()
		s := session(t, newFakeStore(), def("API_URL"))

		if got := state(t, s).Slug; got != "shop" {
			t.Errorf("state slug = %q, want %q", got, "shop")
		}
	})

	t.Run("the matrix names the environments that still override a cell", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.environments = []string{"pr-42"}
		store.override(envgate.Cell{Key: "API_URL"}, "pr-42")
		s := session(t, store, def("API_URL"))

		c := cellState(t, state(t, s), "API_URL", "")
		if c.Set {
			t.Error("the matrix reports API_URL filled, want it empty — pr-42's value is what pr-42 reads, not what the cell holds")
		}
		want := []envgate.Override{{Environment: "pr-42", Version: 1}}
		if !reflect.DeepEqual(c.Overrides, want) {
			t.Errorf("overrides = %+v, want %+v — a surviving override the page cannot see is one it lies about", c.Overrides, want)
		}
	})

	t.Run("the state names the environments an override can be written against", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.environments = []string{"pr-42", "staging"}
		s := session(t, store, def("API_URL"))

		if got := state(t, s).Environments; !reflect.DeepEqual(got, store.environments) {
			t.Errorf("environments = %q, want %q", got, store.environments)
		}
	})

	t.Run("an orphaned override is marked and still removable", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.override(envgate.Cell{Key: "API_URL"}, "pr-42")
		s := session(t, store, def("API_URL"))

		c := cellState(t, state(t, s), "API_URL", "")
		if len(c.Overrides) != 1 || !c.Overrides[0].Orphaned {
			t.Fatalf("overrides = %+v, want pr-42 marked orphaned: no environment by that name exists", c.Overrides)
		}

		resp := request(t, s, http.MethodDelete, "/api/value?key=API_URL&folder=&environment=pr-42", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("DELETE = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}
		if got := cellState(t, state(t, s), "API_URL", "").Overrides; len(got) != 0 {
			t.Errorf("overrides = %+v, want the orphan gone", got)
		}
	})
}

func TestPutValue(t *testing.T) {
	t.Parallel()

	t.Run("a write to a forbidden cell is refused with the reason and never reaches the store", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		s := session(t, store, def("POSTHOG_ID", "/web"))

		resp := request(t, s, http.MethodPut, "/api/value", map[string]string{"key": "POSTHOG_ID", "folder": "", "value": "ph_root"})

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT to a forbidden cell = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
		if body := bodyOf(t, resp); !strings.Contains(body, "scoped to /web") {
			t.Errorf("refusal = %q, want it to name the scope that forbids the cell", body)
		}
		if len(store.cells) != 0 {
			t.Errorf("store holds %v, want nothing", store.cells)
		}
	})

	t.Run("a write to an unaddressable folder is refused and never reaches the store", func(t *testing.T) {
		t.Parallel()
		for _, folder := range []string{"web", "/", "/web/", "/web//api"} {
			t.Run(folder, func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				s := session(t, store, def("API_URL"))

				resp := request(t, s, http.MethodPut, "/api/value", map[string]string{"key": "API_URL", "folder": folder, "value": "https://x.example"})

				if resp.StatusCode != http.StatusBadRequest {
					t.Errorf("PUT to folder %q = %d, want %d", folder, resp.StatusCode, http.StatusBadRequest)
				}
				if len(store.cells) != 0 {
					t.Errorf("store holds %v, want nothing — nothing resolves a value in %q", store.cells, folder)
				}
			})
		}
	})

	t.Run("a write to a permitted cell reaches the store and the matrix shows it filled", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		s := session(t, store, def("POSTHOG_ID", "/web"))

		resp := request(t, s, http.MethodPut, "/api/value", map[string]string{"key": "POSTHOG_ID", "folder": "/web", "value": "ph_web"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}

		if got := store.cells[envgate.Cell{Key: "POSTHOG_ID", Folder: "/web"}]; got != "ph_web" {
			t.Errorf("store holds %q for POSTHOG_ID in /web, want %q", got, "ph_web")
		}
		if !cellState(t, state(t, s), "POSTHOG_ID", "/web").Set {
			t.Error("the matrix still reports POSTHOG_ID in /web unset, want it filled without a restart")
		}
	})

	t.Run("a write clears the complaint about the value it replaced", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.cells[envgate.Cell{Key: "API_URL"}] = "not-a-url"
		gate := discovered(t, store, def("API_URL"))
		if _, err := gate.ReportEnvProblems(context.Background(), &resourcesv1.ReportEnvProblemsRequest{
			Problems: []*resourcesv1.VariableProblem{{Key: "API_URL", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "must be a URL"}},
		}); err != nil {
			t.Fatalf("ReportEnvProblems: %v", err)
		}
		s := serve(t, store, gate)
		if cellState(t, state(t, s), "API_URL", "").Problem == "" {
			t.Fatal("the cell carries no complaint before the fix, so the test cannot show one being cleared")
		}

		resp := request(t, s, http.MethodPut, "/api/value", map[string]string{"key": "API_URL", "folder": "", "value": "https://ok.example"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}

		if got := cellState(t, state(t, s), "API_URL", "").Problem; got != "" {
			t.Errorf("cell still complains %q, want it cleared — that complaint was about a value that is gone", got)
		}
	})

	t.Run("an override is written beside the class-wide value rather than over it", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.environments = []string{"staging"}
		store.cells[envgate.Cell{Key: "API_URL"}] = "https://shared.example"
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodPut, "/api/value", map[string]any{
			"key": "API_URL", "folder": "", "environment": "staging", "value": "https://staging.example",
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}

		at := envgate.Address{Cell: envgate.Cell{Key: "API_URL"}, Environment: "staging"}
		if got := store.held[at]; got != "https://staging.example" {
			t.Errorf("staging holds %q, want the override that was written", got)
		}
		if got := store.cells[envgate.Cell{Key: "API_URL"}]; got != "https://shared.example" {
			t.Errorf("the class-wide value is %q, want it untouched", got)
		}
	})

	t.Run("refuses an override against an environment that does not exist", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.environments = []string{"staging"}
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodPut, "/api/value", map[string]any{
			"key": "API_URL", "folder": "", "environment": "stagng", "value": "https://typo.example",
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PUT = %d: %s, want it refused", resp.StatusCode, bodyOf(t, resp))
		}
		if len(store.held) != 0 {
			t.Errorf("store holds %v, want the refused write to have landed nowhere", store.held)
		}
	})

	t.Run("an override write expects the version rendered for its own environment", func(t *testing.T) {
		t.Parallel()
		at := envgate.Address{Cell: envgate.Cell{Key: "API_URL"}, Environment: "staging"}

		for _, tc := range []struct {
			name       string
			version    int64
			wantStatus int
		}{
			{name: "the version the page rendered writes", version: 1, wantStatus: http.StatusOK},
			{name: "a version somebody has moved past is a conflict", version: 0, wantStatus: http.StatusConflict},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				store.environments = []string{"staging"}
				store.cells[envgate.Cell{Key: "API_URL"}] = "https://shared.example"
				store.versions[envgate.Cell{Key: "API_URL"}] = 9
				store.override(at.Cell, at.Environment)
				s := session(t, store, def("API_URL"))

				resp := request(t, s, http.MethodPut, "/api/value", map[string]any{
					"key": "API_URL", "folder": "", "environment": "staging",
					"value": "https://mine.example", "version": tc.version,
				})

				if resp.StatusCode != tc.wantStatus {
					t.Fatalf("PUT = %d, want %d: %s", resp.StatusCode, tc.wantStatus, bodyOf(t, resp))
				}
				held := store.held[at]
				if tc.wantStatus == http.StatusOK && held != "https://mine.example" {
					t.Errorf("staging holds %q, want the write that quoted the current version", held)
				}
				if tc.wantStatus == http.StatusConflict && held != "override" {
					t.Errorf("staging holds %q, want the value already there — a stale write must not be applied", held)
				}
			})
		}
	})

	t.Run("a write expects the version the page rendered", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.cells[envgate.Cell{Key: "API_URL"}] = "https://old.example"
		store.versions[envgate.Cell{Key: "API_URL"}] = 3
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodPut, "/api/value", map[string]any{
			"key": "API_URL", "folder": "", "value": "https://new.example", "version": 3,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}

		if len(store.expected) != 1 || store.expected[0] == nil || *store.expected[0] != 3 {
			t.Errorf("store saw expectations %v, want the one version the page rendered", format(store.expected))
		}
	})

	t.Run("a write to a cell the page drew empty expects no live value", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodPut, "/api/value", map[string]any{
			"key": "API_URL", "folder": "", "value": "https://new.example", "version": 0,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}

		if len(store.expected) != 1 || store.expected[0] == nil || *store.expected[0] != 0 {
			t.Errorf("store saw expectations %v, want a zero expectation — an empty cell must be written only while it is still empty", format(store.expected))
		}
	})

	t.Run("a write that quotes no version is blind", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodPut, "/api/value", map[string]string{"key": "API_URL", "folder": "", "value": "https://new.example"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}

		if len(store.expected) != 1 || store.expected[0] != nil {
			t.Errorf("store saw expectations %v, want none — a caller that read no version cannot claim one", format(store.expected))
		}
	})

	t.Run("a write against a version that is no longer current is refused as a conflict", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.cells[envgate.Cell{Key: "API_URL"}] = "https://someone-elses.example"
		store.versions[envgate.Cell{Key: "API_URL"}] = 4
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodPut, "/api/value", map[string]any{
			"key": "API_URL", "folder": "", "value": "https://mine.example", "version": 3,
		})

		if resp.StatusCode != http.StatusConflict {
			t.Errorf("stale PUT = %d, want %d — a refused write is a conflict, not a failed store", resp.StatusCode, http.StatusConflict)
		}
		if got := store.cells[envgate.Cell{Key: "API_URL"}]; got != "https://someone-elses.example" {
			t.Errorf("store holds %q, want the value already there — a stale write must not be applied", got)
		}
	})
}

func TestDeleteValue(t *testing.T) {
	t.Parallel()

	t.Run("a delete of an unaddressable folder is refused and never reaches the store", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodDelete, "/api/value?key=API_URL&folder=web", nil)

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("DELETE in folder %q = %d, want %d", "web", resp.StatusCode, http.StatusBadRequest)
		}
		if store.deletes != 0 {
			t.Errorf("store saw %d deletes, want none — a folder the store cannot address names no cell to delete", store.deletes)
		}
	})

	t.Run("deleting a cell empties it in the store and the matrix", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.cells[envgate.Cell{Key: "API_URL"}] = "https://root.example"
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodDelete, "/api/value?key=API_URL&folder=", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("DELETE = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}

		if len(store.cells) != 0 {
			t.Errorf("store holds %v, want nothing", store.cells)
		}
		if cellState(t, state(t, s), "API_URL", "").Set {
			t.Error("the matrix still reports API_URL filled, want it empty")
		}
	})

	t.Run("a delete against a version that is no longer current is refused as a conflict", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.cells[envgate.Cell{Key: "API_URL"}] = "https://someone-elses.example"
		store.versions[envgate.Cell{Key: "API_URL"}] = 4
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodDelete, "/api/value?key=API_URL&folder=&version=3", nil)

		if resp.StatusCode != http.StatusConflict {
			t.Errorf("stale DELETE = %d, want %d — a refused delete is a conflict, not a failed store", resp.StatusCode, http.StatusConflict)
		}
		if got := store.cells[envgate.Cell{Key: "API_URL"}]; got != "https://someone-elses.example" {
			t.Errorf("store holds %q, want the value already there — a stale delete must not be applied", got)
		}
	})

	t.Run("a delete expects the version the page rendered", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.cells[envgate.Cell{Key: "API_URL"}] = "https://old.example"
		store.versions[envgate.Cell{Key: "API_URL"}] = 3
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodDelete, "/api/value?key=API_URL&folder=&version=3", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("DELETE = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}

		if len(store.expected) != 1 || store.expected[0] == nil || *store.expected[0] != 3 {
			t.Errorf("store saw expectations %v, want the one version the page rendered", format(store.expected))
		}
	})

	t.Run("a delete quoting an unreadable version is refused and never reaches the store", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.cells[envgate.Cell{Key: "API_URL"}] = "https://old.example"
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodDelete, "/api/value?key=API_URL&folder=&version=soon", nil)

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("DELETE quoting a version that is not a number = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
		if store.deletes != 0 {
			t.Errorf("store saw %d deletes, want none — an expectation that cannot be read is not an absent one", store.deletes)
		}
	})
}

func TestGetHistory(t *testing.T) {
	t.Parallel()

	t.Run("history is readable newest first", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		store.cells[envgate.Cell{Key: "API_URL"}] = "https://root.example"
		s := session(t, store, def("API_URL"))

		resp := request(t, s, http.MethodGet, "/api/history?key=API_URL&folder=", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/history = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}
		var out struct {
			Versions []varsui.Version `json:"versions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode history: %v", err)
		}

		if len(out.Versions) != 2 || out.Versions[0].Version != 2 {
			t.Errorf("history = %+v, want two entries newest first", out.Versions)
		}
	})
}

func TestSessionWait(t *testing.T) {
	t.Parallel()

	t.Run("returns when the developer says the matrix is done", func(t *testing.T) {
		t.Parallel()
		s := session(t, newFakeStore(), def("API_URL"))

		type posted struct {
			resp *http.Response
			err  error
		}
		answers := make(chan posted, 1)
		req := newRequest(t, s, http.MethodPost, "/api/done", nil)
		go func() {
			resp, err := http.DefaultClient.Do(req)
			answers <- posted{resp, err}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Wait(ctx); err != nil {
			t.Errorf("Wait = %v, want nil — the developer finished", err)
		}
		if err := s.Wait(context.Background()); err != nil {
			t.Errorf("second Wait = %v, want nil — a completed matrix stays complete however often the caller looks", err)
		}

		answer := <-answers
		if answer.err != nil {
			t.Fatalf("POST /api/done: %v — the page must be told the matrix was accepted before the session goes away", answer.err)
		}
		defer answer.resp.Body.Close()
		if answer.resp.StatusCode != http.StatusOK {
			t.Errorf("POST /api/done = %d, want %d", answer.resp.StatusCode, http.StatusOK)
		}
	})

	t.Run("returns the interruption even after the watcher has already closed the session", func(t *testing.T) {
		t.Parallel()
		for round := range 30 {
			ctx, cancel := context.WithCancel(context.Background())
			store := newFakeStore()
			s := serveUnder(t, ctx, store, discovered(t, store, def("API_URL")))

			cancel()
			awaitStopped(t, s)

			if err := s.Wait(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("round %d: Wait = %v, want context.Canceled — an interrupted deploy must not be trapped waiting on a browser, nor told it may go on", round, err)
			}
		}
	})

	t.Run("keeps reporting an abandoned session on a fresh context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := newFakeStore()
		s := serveUnder(t, ctx, store, discovered(t, store, def("API_URL")))

		cancel()
		awaitStopped(t, s)

		err := s.Wait(context.Background())
		if err == nil {
			t.Fatal("Wait = nil on a session nobody completed, want an abandonment — a deploy would resume the command the developer killed")
		}
		if !errors.Is(err, varsui.ErrAbandoned) {
			t.Errorf("Wait = %v, want varsui.ErrAbandoned so a caller can tell an abandoned session from a completed one", err)
		}
	})
}

func TestSessionClose(t *testing.T) {
	t.Run("closing a session stops it watching the caller's context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const sessions, slack = 30, 10
		before := runtime.NumGoroutine()
		for range sessions {
			store := newFakeStore()
			s := serveUnder(t, ctx, store, discovered(t, store, def("API_URL")))
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}

		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > before+slack && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if got := runtime.NumGoroutine(); got > before+slack {
			t.Errorf("%d goroutines after closing %d sessions, up from %d — a closed session still watches a context it can no longer act on", got, sessions, before)
		}
	})
}
