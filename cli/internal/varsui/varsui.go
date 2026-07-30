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
	"sync"

	"github.com/ocelhq/ocel/cli/internal/envgate"
)

// Store is the value store as the UI writes to it. Reads come through the
// gate, which already holds the project's cells; these are the operations that
// change them.
type Store interface {
	Set(ctx context.Context, cell envgate.Cell, value string) error
	Delete(ctx context.Context, cell envgate.Cell) error
	History(ctx context.Context, cell envgate.Cell) ([]Version, error)
}

// Version is one entry of a cell's change history. It carries no plaintext:
// history in the UI answers when a value changed, and revealing a past value
// is a deliberate act reserved for `ocel env history --reveal`.
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

	done     chan struct{}
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
		<-ctx.Done()
		_ = s.Close()
	}()
	return s, nil
}

// Wait blocks until the developer marks the matrix complete, or the caller's
// context ends — an interrupted deploy is never trapped waiting on a browser.
func (s *Session) Wait(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops serving and releases anyone in Wait.
func (s *Session) Close() error {
	var err error
	s.closeOne.Do(func() {
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
	s.writeState(w, r.Context())
}

type valueRequest struct {
	Key    string `json:"key"`
	Folder string `json:"folder"`
	Value  string `json:"value"`
}

func (s *Session) handleSet(w http.ResponseWriter, r *http.Request) {
	var req valueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	cell := envgate.Cell{Key: req.Key, Folder: req.Folder}

	// The same check the CLI's own write path makes. The matrix draws a
	// forbidden cell unfillable, so reaching here means something bypassed the
	// page — and the rule holds anyway.
	if err := envgate.CheckWritable(s.opts.Gate.Definitions(), cell.Key, cell.Folder); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.opts.Store.Set(r.Context(), cell, req.Value); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}

	// Discovery's complaint was about the value that was there. Whatever is
	// wrong with the new one is the next discovery run's to find.
	s.opts.Gate.Forget(cell)
	s.writeState(w, r.Context())
}

func (s *Session) handleDelete(w http.ResponseWriter, r *http.Request) {
	cell := queryCell(r)
	if err := s.opts.Store.Delete(r.Context(), cell); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.opts.Gate.Forget(cell)
	s.writeState(w, r.Context())
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

func (s *Session) handleDone(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	go func() { _ = s.Close() }()
}

func queryCell(r *http.Request) envgate.Cell {
	return envgate.Cell{Key: r.URL.Query().Get("key"), Folder: r.URL.Query().Get("folder")}
}

// writeState re-reads the store before answering, so every mutation returns
// the matrix as it now stands and the page never renders from its own guess.
func (s *Session) writeState(w http.ResponseWriter, ctx context.Context) {
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

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		return
	}
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
