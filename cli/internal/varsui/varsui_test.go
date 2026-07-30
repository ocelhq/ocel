package varsui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// fakeStore is both halves of what a session needs: the cells the gate reads
// and the writes the UI makes, sharing one map so a write is visible to the
// next read exactly as it is through a provider.
type fakeStore struct {
	cells  map[envgate.Cell]string
	setErr error
}

func newFakeStore() *fakeStore { return &fakeStore{cells: map[envgate.Cell]string{}} }

func (s *fakeStore) List(context.Context) ([]envgate.Cell, error) {
	out := make([]envgate.Cell, 0, len(s.cells))
	for cell := range s.cells {
		out = append(out, cell)
	}
	return out, nil
}

func (s *fakeStore) Reveal(_ context.Context, cell envgate.Cell) (string, bool, error) {
	value, ok := s.cells[cell]
	return value, ok, nil
}

func (s *fakeStore) Set(_ context.Context, cell envgate.Cell, value string) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.cells[cell] = value
	return nil
}

func (s *fakeStore) Delete(_ context.Context, cell envgate.Cell) error {
	delete(s.cells, cell)
	return nil
}

func (s *fakeStore) History(_ context.Context, cell envgate.Cell) ([]varsui.Version, error) {
	if _, ok := s.cells[cell]; !ok {
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

// discovered is a gate that has run discovery over the given declarations, as
// a deploy or an `ocel env` command leaves it.
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

// session starts a real loopback server over the given declarations, the way
// every caller does.
func session(t *testing.T, store *fakeStore, definitions ...*resourcesv1.VariableDefinition) *varsui.Session {
	t.Helper()
	return serve(t, store, discovered(t, store, definitions...))
}

func serve(t *testing.T, store *fakeStore, gate *envgate.Gate) *varsui.Session {
	t.Helper()
	s, err := varsui.Serve(context.Background(), varsui.Options{
		Assets: fstest.MapFS{"index.html": {Data: []byte("<title>vars</title>")}},
		Gate:   gate,
		Store:  store,
		Slug:   "shop",
	})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// origin is the scheme and authority the session's own page is served from —
// what every legitimate API request carries and nothing else can forge.
func origin(s *varsui.Session) string {
	return strings.SplitN(s.URL, "#", 2)[0]
}

// request is an API call exactly as the session's own page makes it.
func request(t *testing.T, s *varsui.Session, method, path string, body any) *http.Response {
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
	return do(t, req)
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

func TestSession_BindsLoopbackOnly(t *testing.T) {
	s := session(t, newFakeStore(), def("API_URL"))

	if !strings.HasPrefix(s.URL, "http://127.0.0.1:") {
		t.Errorf("session URL = %q, want a loopback address — a store-writing server must not be reachable off this host", s.URL)
	}
}

func TestSession_TheLaunchURLCarriesTheTokenInTheFragment(t *testing.T) {
	s := session(t, newFakeStore(), def("API_URL"))

	if !strings.HasSuffix(s.URL, "#t="+s.Token) {
		t.Errorf("session URL = %q, want the token in the fragment so it never travels in a request line or a Referer", s.URL)
	}
	if len(s.Token) < 32 {
		t.Errorf("token = %q (%d chars), want one long enough not to be guessed", s.Token, len(s.Token))
	}
}

func TestSession_ARequestWithoutTheTokenIsRefused(t *testing.T) {
	store := newFakeStore()
	s := session(t, store, def("API_URL"))

	req, err := http.NewRequest(http.MethodPut, origin(s)+"/api/value", strings.NewReader(`{"key":"API_URL","folder":"","value":"x"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp := do(t, req)

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("untokened PUT = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if len(store.cells) != 0 {
		t.Errorf("store holds %v, want nothing — a refused request must not have written", store.cells)
	}
}

func TestSession_ARequestWithTheWrongTokenIsRefused(t *testing.T) {
	s := session(t, newFakeStore(), def("API_URL"))

	req, err := http.NewRequest(http.MethodGet, origin(s)+"/api/state", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", len(s.Token)))
	resp := do(t, req)

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong-token GET = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

// A page on another origin cannot read our fragment, but it can still aim a
// request at the port. Rejecting a mismatched Origin closes that door even if
// a token ever leaked.
func TestSession_ARequestFromAnotherOriginIsRefused(t *testing.T) {
	store := newFakeStore()
	s := session(t, store, def("API_URL"))

	req, err := http.NewRequest(http.MethodPut, origin(s)+"/api/value", strings.NewReader(`{"key":"API_URL","folder":"","value":"x"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp := do(t, req)

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin PUT = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if len(store.cells) != 0 {
		t.Errorf("store holds %v, want nothing", store.cells)
	}
}

// A rebinding attack reaches the port under a name that resolves to loopback
// only after the page has loaded, so the Host header is the tell.
func TestSession_ARequestUnderAnotherHostnameIsRefused(t *testing.T) {
	s := session(t, newFakeStore(), def("API_URL"))

	req, err := http.NewRequest(http.MethodGet, origin(s)+"/api/state", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Host = "rebound.example"
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp := do(t, req)

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rebound GET = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestSession_ServesTheEmbeddedPageWithoutATokenAndReadsTheMatrixWithOne(t *testing.T) {
	s := session(t, newFakeStore(), def("API_URL"))

	resp := do(t, mustGet(t, s.URL))
	if resp.StatusCode != http.StatusOK || !strings.Contains(bodyOf(t, resp), "<title>vars</title>") {
		t.Errorf("GET / = %d, want the embedded page — it holds no secrets and must load before the token is read", resp.StatusCode)
	}
	if got := state(t, s).Slug; got != "shop" {
		t.Errorf("state slug = %q, want %q", got, "shop")
	}
}

func TestSession_AWriteToAForbiddenCellIsRefusedWithTheReasonAndNeverReachesTheStore(t *testing.T) {
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
}

func TestSession_AWriteToAPermittedCellReachesTheStoreAndTheMatrixShowsItFilled(t *testing.T) {
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
}

func TestSession_AWriteClearsTheComplaintAboutTheValueItReplaced(t *testing.T) {
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
}

func TestSession_DeletingACellEmptiesItInTheStoreAndTheMatrix(t *testing.T) {
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
}

func TestSession_HistoryIsReadableNewestFirst(t *testing.T) {
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
}

func TestSession_WaitReturnsWhenTheDeveloperSaysTheMatrixIsDone(t *testing.T) {
	s := session(t, newFakeStore(), def("API_URL"))

	go func() {
		resp := request(t, s, http.MethodPost, "/api/done", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("POST /api/done = %d", resp.StatusCode)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Wait(ctx); err != nil {
		t.Errorf("Wait = %v, want nil — the developer finished", err)
	}
}

func TestSession_WaitReturnsTheInterruptionWhenTheCallerGivesUp(t *testing.T) {
	s := session(t, newFakeStore(), def("API_URL"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.Wait(ctx); err != context.Canceled {
		t.Errorf("Wait = %v, want context.Canceled — an interrupted deploy must not be trapped waiting on a browser", err)
	}
}

func mustGet(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}
