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
	"strconv"
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
	cells     map[envgate.Cell]string
	versions  map[envgate.Cell]int64
	overrides []envgate.Stored
	expected  []*int64
	deletes   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{cells: map[envgate.Cell]string{}, versions: map[envgate.Cell]int64{}}
}

func (s *fakeStore) override(cell envgate.Cell, environment string) {
	s.overrides = append(s.overrides, envgate.Stored{Cell: cell, Environment: environment, Version: 1})
}

func (s *fakeStore) List(context.Context) ([]envgate.Stored, error) {
	out := make([]envgate.Stored, 0, len(s.cells)+len(s.overrides))
	for cell := range s.cells {
		out = append(out, envgate.Stored{Cell: cell, Version: s.versions[cell]})
	}
	return append(out, s.overrides...), nil
}

func (s *fakeStore) Reveal(_ context.Context, rows []envgate.Read) (map[envgate.Cell]string, error) {
	found := map[envgate.Cell]string{}
	for _, row := range rows {
		if value, ok := s.cells[row.Cell]; ok {
			found[row.Cell] = value
		}
	}
	return found, nil
}

// stale is the store's whole concurrency rule: an expectation that no longer
// describes the cell refuses the operation. Both mutations run it, so a session
// that dropped a version on its way through is a test failure rather than a
// fixture that was going to refuse regardless.
func (s *fakeStore) stale(cell envgate.Cell, expected *int64) bool {
	return expected != nil && *expected != s.versions[cell]
}

func (s *fakeStore) Set(_ context.Context, cell envgate.Cell, value string, expected *int64) error {
	s.expected = append(s.expected, expected)
	if s.stale(cell, expected) {
		return varsui.ErrStaleValue
	}
	s.cells[cell] = value
	return nil
}

func (s *fakeStore) Delete(_ context.Context, cell envgate.Cell, expected *int64) error {
	s.expected = append(s.expected, expected)
	if s.stale(cell, expected) {
		return varsui.ErrStaleValue
	}
	s.deletes++
	delete(s.cells, cell)
	delete(s.versions, cell)
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
	return serveUnder(t, context.Background(), store, gate)
}

// serveUnder starts a session under a context the test can end, which is how a
// deploy hands the UI its own cancellation.
func serveUnder(t *testing.T, ctx context.Context, store *fakeStore, gate *envgate.Gate) *varsui.Session {
	t.Helper()
	s, err := varsui.Serve(ctx, varsui.Options{
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

// Root is the empty folder everywhere above the store, and a folder is matched
// whole. A cell addressed any other way is one no app could ever resolve, so
// the UI refuses it exactly as `ocel env set` does rather than writing a value
// that is invisible from the moment it lands.
func TestSession_AWriteToAnUnaddressableFolderIsRefusedAndNeverReachesTheStore(t *testing.T) {
	for _, folder := range []string{"web", "/", "/web/", "/web//api"} {
		t.Run(folder, func(t *testing.T) {
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
}

func TestSession_ADeleteOfAnUnaddressableFolderIsRefusedAndNeverReachesTheStore(t *testing.T) {
	store := newFakeStore()
	s := session(t, store, def("API_URL"))

	resp := request(t, s, http.MethodDelete, "/api/value?key=API_URL&folder=web", nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("DELETE in folder %q = %d, want %d", "web", resp.StatusCode, http.StatusBadRequest)
	}
	if store.deletes != 0 {
		t.Errorf("store saw %d deletes, want none — a folder the store cannot address names no cell to delete", store.deletes)
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

func TestSession_TheMatrixNamesTheEnvironmentsThatStillOverrideACell(t *testing.T) {
	store := newFakeStore()
	store.override(envgate.Cell{Key: "API_URL"}, "pr-42")
	s := session(t, store, def("API_URL"))

	c := cellState(t, state(t, s), "API_URL", "")
	if c.Set {
		t.Error("the matrix reports API_URL filled, want it empty — pr-42's value is not the one a deploy reads")
	}
	if !reflect.DeepEqual(c.Overrides, []string{"pr-42"}) {
		t.Errorf("overrides = %q, want [pr-42] — a surviving override the page cannot see is one it lies about", c.Overrides)
	}
}

func TestSession_AWriteExpectsTheVersionThePageRendered(t *testing.T) {
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
}

// Zero is not "no expectation": the store reads it as "no live value", so a
// cell the page drew empty is written under exactly that condition and loses
// to anyone who filled it in between. Absence is the blind write, and only a
// caller that never rendered a version sends that.
func TestSession_AWriteToACellThePageDrewEmptyExpectsNoLiveValue(t *testing.T) {
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
}

func TestSession_AWriteThatQuotesNoVersionIsBlind(t *testing.T) {
	store := newFakeStore()
	s := session(t, store, def("API_URL"))

	resp := request(t, s, http.MethodPut, "/api/value", map[string]string{"key": "API_URL", "folder": "", "value": "https://new.example"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, bodyOf(t, resp))
	}

	if len(store.expected) != 1 || store.expected[0] != nil {
		t.Errorf("store saw expectations %v, want none — a caller that read no version cannot claim one", format(store.expected))
	}
}

// Two people editing one cell is the case optimistic concurrency exists for.
// The second write must be refused and the page told its view was stale, not
// told the store is unreachable and not silently allowed to win.
func TestSession_AWriteAgainstAVersionThatIsNoLongerCurrentIsRefusedAsAConflict(t *testing.T) {
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
}

// Remove is the other half of the same race. A page whose cell was replaced
// while it sat open must not be able to delete the replacement, so the delete
// carries the version it rendered and is refused when that no longer holds.
func TestSession_ADeleteAgainstAVersionThatIsNoLongerCurrentIsRefusedAsAConflict(t *testing.T) {
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
}

func TestSession_ADeleteExpectsTheVersionThePageRendered(t *testing.T) {
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
}

func TestSession_ADeleteQuotingAnUnreadableVersionIsRefusedAndNeverReachesTheStore(t *testing.T) {
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

	// The page posts while the caller is blocked in Wait, which is the only
	// order this ever happens in, and it has to get its answer: finishing tears
	// the server down, and a page that reads that as a failed request would
	// tell the developer their completed matrix did not take.
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
}

// The interruption has to be legible however the race falls. Cancelling closes
// the session from a watcher goroutine, so by the time a caller reaches Wait
// the session may already be closed — and a session closed by an interruption
// looks exactly like one closed by a developer who finished, unless Wait can
// tell them apart. Each round leaves the gap the scheduler would otherwise
// close for us, and there are enough rounds that passing is not luck.
func TestSession_WaitReturnsTheInterruptionEvenAfterTheWatcherHasAlreadyClosedTheSession(t *testing.T) {
	for round := range 30 {
		ctx, cancel := context.WithCancel(context.Background())
		store := newFakeStore()
		s := serveUnder(t, ctx, store, discovered(t, store, def("API_URL")))

		cancel()
		awaitStopped(t, s)

		if err := s.Wait(ctx); err != context.Canceled {
			t.Fatalf("round %d: Wait = %v, want context.Canceled — an interrupted deploy must not be trapped waiting on a browser, nor told it may go on", round, err)
		}
	}
}

// The deploy this UI interrupts resumes only on a completed matrix, and it may
// look again on a context of its own. A session that ended without the
// developer finishing must keep saying so, or a killed deploy resumes.
func TestSession_WaitKeepsReportingAnAbandonedSessionOnAFreshContext(t *testing.T) {
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
}

// A command may open the UI more than once — a gate that fails again after a
// fix reopens it — under a context that lives as long as the command. Each
// closed session has to let go of that context, or the command accumulates a
// goroutine per session for the rest of its run.
func TestSession_ClosingASessionStopsItWatchingTheCallersContext(t *testing.T) {
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
}

// awaitStopped blocks until the session's listener stops accepting, which is
// how a test observes that the watcher's Close has already run.
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
