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
	"slices"
	"strconv"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/envgate"
)

var ErrAbandoned = errors.New("variables UI abandoned")

var ErrStaleValue = errors.New("stale value")

const AbandonedMessage = "the variables UI closed before the matrix was complete"

type Store interface {
	Set(ctx context.Context, at envgate.Address, value string, expected *int64) error
	Delete(ctx context.Context, at envgate.Address, expected *int64) error
	History(ctx context.Context, at envgate.Address) ([]Version, error)
}

type Version struct {
	Version   int64 `json:"version"`
	CreatedAt int64 `json:"createdAt"`
	Size      int64 `json:"size"`
}

type State struct {
	Slug         string         `json:"slug"`
	Bootstrap    string         `json:"bootstrap"`
	Environments []string       `json:"environments"`
	Matrix       envgate.Matrix `json:"matrix"`
}

type Options struct {
	Assets fs.FS

	Gate *envgate.Gate

	Store   Store
	Slug    string
	Preview bool

	Environments []string
}

type Session struct {
	URL   string
	Token string

	opts     Options
	listener net.Listener
	server   *http.Server

	done     chan struct{}
	outcome  error
	closeOne sync.Once
}

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

func (s *Session) Wait(ctx context.Context) error {
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
	mux.Handle("/", page)
	mux.Handle("/api/", s.guard(api))
	return mux
}

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
	Key         string `json:"key"`
	Folder      string `json:"folder"`
	Environment string `json:"environment"`
	Value       string `json:"value"`
	Version     *int64 `json:"version"`
}

func (s *Session) handleSet(w http.ResponseWriter, r *http.Request) {
	var req valueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	at := envgate.Address{Cell: envgate.Cell{Key: req.Key, Folder: req.Folder}, Environment: req.Environment}

	if err := addressable(at.Cell.Folder); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := envgate.CheckWritable(s.opts.Gate.Definitions(), at.Cell.Key, at.Cell.Folder); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.writable(at.Environment); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.opts.Store.Set(r.Context(), at, req.Value, req.Version); err != nil {
		if errors.Is(err, ErrStaleValue) {
			fail(w, http.StatusConflict, err)
			return
		}
		fail(w, http.StatusBadGateway, err)
		return
	}

	s.forget(at)
	s.writeState(r.Context(), w)
}

func (s *Session) handleDelete(w http.ResponseWriter, r *http.Request) {
	at := queryAddress(r)
	if err := addressable(at.Cell.Folder); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	expected, err := queryVersion(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.opts.Store.Delete(r.Context(), at, expected); err != nil {
		if errors.Is(err, ErrStaleValue) {
			fail(w, http.StatusConflict, err)
			return
		}
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.forget(at)
	s.writeState(r.Context(), w)
}

func (s *Session) writable(environment string) error {
	if environment == "" || slices.Contains(s.opts.Environments, environment) {
		return nil
	}
	return fmt.Errorf("no environment named %q exists, so nothing would ever read that value", environment)
}

func (s *Session) forget(at envgate.Address) {
	if at.Environment == "" {
		s.opts.Gate.Forget(at.Cell)
	}
}

func (s *Session) handleHistory(w http.ResponseWriter, r *http.Request) {
	versions, err := s.opts.Store.History(r.Context(), queryAddress(r))
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
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() { _ = s.finish(nil) }()
}

func addressable(folder string) error {
	if folder == "" {
		return nil
	}
	return envgate.ValidateFolder(folder)
}

func queryAddress(r *http.Request) envgate.Address {
	return envgate.Address{
		Cell:        envgate.Cell{Key: r.URL.Query().Get("key"), Folder: r.URL.Query().Get("folder")},
		Environment: r.URL.Query().Get("environment"),
	}
}

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

func (s *Session) writeState(ctx context.Context, w http.ResponseWriter) {
	if err := s.opts.Gate.Prefetch(ctx); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, State{
		Slug:         s.opts.Slug,
		Bootstrap:    s.bootstrap(),
		Environments: s.opts.Environments,
		Matrix:       s.opts.Gate.Matrix(s.opts.Environments),
	})
}

func (s *Session) bootstrap() string {
	if s.opts.Preview {
		return "preview"
	}
	return "production"
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
