// Package varsui serves the bundled variables UI: the required-cell matrix as
// a page, plus the small API that page writes values through.
//
// It runs beside whatever command launched it and shares that command's
// provider session, because the provider is the only component that can reach
// the store. Nothing here is reachable off this host: the listener is
// loopback, every API request must carry the session's token, and a request
// whose Origin or Host is not this session's own is refused. A local server
// that can write secrets to a cloud store is a target for DNS rebinding and
// drive-by requests, so those checks are the package's contract, not a
// hardening pass over it.
package varsui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/envgate"
)

// ErrAbandoned is what Wait reports for a session that ended without the
// developer marking the matrix complete. A caller that resumes work on a
// completed matrix must treat this as a refusal, not a finish.
var ErrAbandoned = errors.New("the variables UI closed before the matrix was complete")

// ErrStaleValue is what a Store reports for a write whose expectation about the
// current version no longer holds. The page that sent it was showing a value
// somebody else has since changed, so the answer is to show it again — not to
// apply the write, and not to report the store broken.
var ErrStaleValue = errors.New("this value changed since the page read it; the page is showing it again — make your change against the value that is there now")

// Store is the value store as the UI writes to it. Reads come through the
// gate, which already holds the project's cells; these are the operations that
// change them.
type Store interface {
	// Set writes a value and Delete unsets one. expected is the version the page
	// rendered: zero for a cell it drew empty, which the store honours as "no
	// live value", and nil only for a caller that rendered no version at all. A
	// delete is expectation-bound for the same reason a write is — a page that
	// renders a value somebody has since replaced must not be able to destroy
	// the replacement.
	Set(ctx context.Context, cell envgate.Cell, value string, expected *int64) error
	Delete(ctx context.Context, cell envgate.Cell, expected *int64) error
	History(ctx context.Context, cell envgate.Cell) ([]Version, error)
}

// Version is one entry of a cell's change history. It carries no plaintext:
// history answers when a value changed, not what it was. Reading a value back
// is `ocel env get --reveal`, one named cell at a time.
type Version struct {
	Version   int64 `json:"version"`
	CreatedAt int64 `json:"createdAt"`
	Size      int64 `json:"size"`
}

// State is everything the page renders: which project and substrate it is
// looking at, and the matrix itself.
type State struct {
	Slug      string         `json:"slug"`
	Substrate string         `json:"substrate"`
	Matrix    envgate.Matrix `json:"matrix"`
}

type Options struct {
	// Assets is the built single-page app, with index.html at its root.
	Assets fs.FS

	// Gate is the discovery run this session presents. It is the authority on
	// what is required and what the store holds, so the UI and the deploy can
	// never disagree.
	Gate *envgate.Gate

	Store   Store
	Slug    string
	Preview bool
}

// Session is a running UI. A caller opens URL, then blocks in Wait until the
// developer says the matrix is done or the caller gives up.
type Session struct {
	URL   string
	Token string

	opts     Options
	listener net.Listener
	server   *http.Server

	// done is closed exactly once, by whatever ended the session, and outcome
	// records which that was: nil for a developer who finished, ErrAbandoned
	// for anything else. Wait cannot read a closed channel as a completion.
	done     chan struct{}
	outcome  error
	closeOne sync.Once
}

// Serve binds a loopback listener and starts serving. It returns as soon as
// the address is known, so the caller can print and open the URL while the
// session runs.
func Serve(ctx context.Context, opts Options) (*Session, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open a loopback port for the variables UI: %w", err)
	}

	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		listener.Close()
		return nil, fmt.Errorf("generate the variables UI session token: %w", err)
	}

	s := &Session{
		Token:    base64.RawURLEncoding.EncodeToString(token),
		opts:     opts,
		listener: listener,
		done:     make(chan struct{}),
	}
	// The token rides the fragment, which no browser puts in a request line,
	// a log or a Referer. The page reads it there and sends it as a header.
	s.URL = fmt.Sprintf("http://%s/#t=%s", listener.Addr().String(), s.Token)
	s.server = &http.Server{Handler: s.handler()}

	go func() { _ = s.server.Serve(listener) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
	return s, nil
}

// Wait blocks until the developer marks the matrix complete, or the caller's
// context ends — an interrupted deploy is never trapped waiting on a browser.
// It answers the same way however late it is asked: nil only for a matrix the
// developer finished, and never for a session that ended any other way.
func (s *Session) Wait(ctx context.Context) error {
	// An already-ended context is the interruption, whether or not its watcher
	// has closed the session yet. Reading it first is what makes the answer to
	// a cancelled deploy ctx.Err() every time rather than whichever of two
	// ready channels a select happened to pick.
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-s.done:
		return s.outcome
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops serving and releases anyone in Wait with ErrAbandoned: a session
// closed by anything but the developer left the matrix unfinished.
func (s *Session) Close() error {
	return s.finish(ErrAbandoned)
}

func (s *Session) finish(outcome error) error {
	var err error
	s.closeOne.Do(func() {
		s.outcome = outcome
		close(s.done)
		err = s.server.Close()
	})
	return err
}

func (s *Session) handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/state", s.handleState)
	api.HandleFunc("PUT /api/value", s.handleSet)
	api.HandleFunc("DELETE /api/value", s.handleDelete)
	api.HandleFunc("GET /api/history", s.handleHistory)
	api.HandleFunc("POST /api/done", s.handleDone)

	page := http.FileServerFS(s.opts.Assets)

	mux := http.NewServeMux()
	// The page itself is unguarded: it is the same bytes for every project and
	// holds no value, and it has to load before it can read the token it will
	// then send on every call below.
	mux.Handle("/", page)
	mux.Handle("/api/", s.guard(api))
	return mux
}

// guard is the whole security posture, applied to every API request: the
// session's own address, the session's own page, and the session's own token.
func (s *Session) guard(next http.Handler) http.Handler {
	address := s.listener.Addr().String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != address {
			refuse(w, "this request names a host other than the session's own; a name that resolves here later is not this session")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+address {
			refuse(w, "this request comes from another origin")
			return
		}
		if !s.authorized(r.Header.Get("Authorization")) {
			refuse(w, "this request carries no valid session token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Session) authorized(header string) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(s.Token)) == 1
}

func refuse(w http.ResponseWriter, reason string) {
	http.Error(w, reason, http.StatusForbidden)
}

func (s *Session) handleState(w http.ResponseWriter, r *http.Request) {
	s.writeState(r.Context(), w)
}

type valueRequest struct {
	Key     string `json:"key"`
	Folder  string `json:"folder"`
	Value   string `json:"value"`
	Version *int64 `json:"version"`
}

func (s *Session) handleSet(w http.ResponseWriter, r *http.Request) {
	var req valueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	cell := envgate.Cell{Key: req.Key, Folder: req.Folder}

	// The same checks the CLI's own write path makes. The matrix draws only
	// cells that exist and draws a forbidden one unfillable, so reaching here
	// means something bypassed the page — and the rules hold anyway.
	if err := addressable(cell.Folder); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := envgate.CheckWritable(s.opts.Gate.Definitions(), cell.Key, cell.Folder); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.opts.Store.Set(r.Context(), cell, req.Value, req.Version); err != nil {
		if errors.Is(err, ErrStaleValue) {
			fail(w, http.StatusConflict, err)
			return
		}
		fail(w, http.StatusBadGateway, err)
		return
	}

	// Discovery's complaint was about the value that was there. Whatever is
	// wrong with the new one is the next discovery run's to find.
	s.opts.Gate.Forget(cell)
	s.writeState(r.Context(), w)
}

func (s *Session) handleDelete(w http.ResponseWriter, r *http.Request) {
	cell := queryCell(r)
	if err := addressable(cell.Folder); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	expected, err := queryVersion(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.opts.Store.Delete(r.Context(), cell, expected); err != nil {
		if errors.Is(err, ErrStaleValue) {
			fail(w, http.StatusConflict, err)
			return
		}
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.opts.Gate.Forget(cell)
	s.writeState(r.Context(), w)
}

func (s *Session) handleHistory(w http.ResponseWriter, r *http.Request) {
	versions, err := s.opts.Store.History(r.Context(), queryCell(r))
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if versions == nil {
		versions = []Version{}
	}
	writeJSON(w, map[string]any{"versions": versions})
}

// handleDone is the one path that ends a session as a completion. Closing tears
// down live connections, so the answer goes out before it starts rather than
// leaving the page to read a completed session as a failed request.
func (s *Session) handleDone(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() { _ = s.finish(nil) }()
}

// addressable refuses a folder no app could read a value back from. Root is
// the absence of a folder, spelled as the empty string above the store, so
// that is the one folder ValidateFolder is not asked about.
func addressable(folder string) error {
	if folder == "" {
		return nil
	}
	return envgate.ValidateFolder(folder)
}

func queryCell(r *http.Request) envgate.Cell {
	return envgate.Cell{Key: r.URL.Query().Get("key"), Folder: r.URL.Query().Get("folder")}
}

// queryVersion reads the expectation a delete carries. An absent one is the
// blind delete; an unreadable one is refused rather than dropped, because
// silently blinding a write the page meant to condition is the lost update the
// expectation exists to stop.
func queryVersion(r *http.Request) (*int64, error) {
	raw := r.URL.Query().Get("version")
	if raw == "" {
		return nil, nil
	}
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("read the version this request expects: %w", err)
	}
	return &version, nil
}

// writeState re-reads the store before answering, so every mutation returns
// the matrix as it now stands and the page never renders from its own guess.
func (s *Session) writeState(ctx context.Context, w http.ResponseWriter) {
	if err := s.opts.Gate.Prefetch(ctx); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, State{Slug: s.opts.Slug, Substrate: s.substrate(), Matrix: s.opts.Gate.Matrix()})
}

func (s *Session) substrate() string {
	if s.opts.Preview {
		return "preview"
	}
	return "production"
}

// writeJSON ignores a failed write because by then the only party that could
// be told is the one that stopped listening: the status line is already out
// and this server has no log of its own — it borrows the command's terminal,
// where a note about a browser that navigated away is noise.
func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
