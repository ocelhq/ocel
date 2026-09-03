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
	"time"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

var ErrAbandoned = errors.New("variables UI abandoned")

var ErrStaleValue = errors.New("stale value")

const AbandonedMessage = "the variables UI closed before the matrix was complete"

const DefaultAbsence = 5 * time.Second

type Reader interface {
	List(ctx context.Context) ([]envgate.Stored, error)
	Read(ctx context.Context, rows []envgate.Address) (map[envgate.Address]string, error)
}

type Store interface {
	Reader
	Set(ctx context.Context, at envgate.Address, value string, expected *int64) error
	Delete(ctx context.Context, at envgate.Address, expected *int64) error
	History(ctx context.Context, at envgate.Address) ([]Version, error)
}

type Version struct {
	Version   int64 `json:"version"`
	CreatedAt int64 `json:"createdAt"`
	Size      int64 `json:"size"`
}

type Recovery struct {
	Deploy string         `json:"deploy"`
	Owed   []envgate.Cell `json:"owed"`
}

type State struct {
	Slug         string         `json:"slug"`
	Tier         string         `json:"tier"`
	Other        string         `json:"other"`
	Environments []string       `json:"environments"`
	Matrix       envgate.Matrix `json:"matrix"`
	Recovery     *Recovery      `json:"recovery,omitempty"`
}

type Options struct {
	Assets fs.FS

	Gate *envgate.Gate

	Store Store
	Other Reader

	Slug    string
	Preview bool

	Environments []string

	Recovery *Recovery

	Absence time.Duration
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

	present  sync.Mutex
	pages    int
	departed *time.Timer
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

	if opts.Absence <= 0 {
		opts.Absence = DefaultAbsence
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
	api.HandleFunc("POST /api/reveal", s.handleReveal)
	api.HandleFunc("GET /api/history", s.handleHistory)
	api.HandleFunc("GET /api/other", s.handleOther)
	api.HandleFunc("POST /api/copy", s.handleCopy)
	api.HandleFunc("POST /api/done", s.handleDone)
	api.HandleFunc("POST /api/abandon", s.handleAbandon)
	api.HandleFunc("GET /api/presence", s.handlePresence)

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

type addressRequest struct {
	Key         string `json:"key"`
	Folder      string `json:"folder"`
	Environment string `json:"environment"`
}

func (a addressRequest) address() envgate.Address {
	return envgate.Address{Cell: envgate.Cell{Key: a.Key, Folder: a.Folder}, Environment: a.Environment}
}

func addressOf(at envgate.Address) addressRequest {
	return addressRequest{Key: at.Cell.Key, Folder: at.Cell.Folder, Environment: at.Environment}
}

type valueRequest struct {
	addressRequest
	Value   string `json:"value"`
	Version *int64 `json:"version"`
}

func (s *Session) handleSet(w http.ResponseWriter, r *http.Request) {
	var req valueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	at := req.address()
	if err := s.writable(at); err != nil {
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
	writeJSON(w, struct{}{})
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
	writeJSON(w, struct{}{})
}

func (s *Session) writable(at envgate.Address) error {
	if err := addressable(at.Cell.Folder); err != nil {
		return err
	}
	if err := envgate.CheckWritable(s.opts.Gate.Definitions(), at.Cell.Key, at.Cell.Folder); err != nil {
		return err
	}
	if at.Environment == "" || slices.Contains(s.opts.Environments, at.Environment) {
		return nil
	}
	return fmt.Errorf("no environment named %q exists, so nothing would ever read that value", at.Environment)
}

func (s *Session) forget(at envgate.Address) {
	if at.Environment == "" {
		s.opts.Gate.Forget(at.Cell)
	}
}

type revealRequest struct {
	Cells []addressRequest `json:"cells"`
}

type revealedValue struct {
	addressRequest
	Value string `json:"value"`
}

type cellError struct {
	addressRequest
	Error string `json:"error"`
}

type revealResponse struct {
	Values []revealedValue `json:"values"`
	Errors []cellError     `json:"errors"`
}

func (s *Session) handleReveal(w http.ResponseWriter, r *http.Request) {
	var req revealRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	secrets := s.secrets()
	rows := make([]envgate.Address, 0, len(req.Cells))
	for _, cell := range req.Cells {
		if secrets[cell.Key] {
			fail(w, http.StatusBadRequest, fmt.Errorf("%s is a secret, and a secret's value never reaches a browser; overwrite it here or read it with ocel env get --reveal", cell.Key))
			return
		}
		rows = append(rows, cell.address())
	}
	writeJSON(w, s.read(r.Context(), s.opts.Store, rows))
}

func (s *Session) read(ctx context.Context, from Reader, rows []envgate.Address) revealResponse {
	out := revealResponse{Values: []revealedValue{}, Errors: []cellError{}}
	if len(rows) == 0 {
		return out
	}
	found, err := from.Read(ctx, rows)
	for _, at := range rows {
		switch value, ok := found[at]; {
		case err != nil:
			out.Errors = append(out.Errors, cellError{addressOf(at), err.Error()})
		case !ok:
			out.Errors = append(out.Errors, cellError{addressOf(at), "no value could be read here"})
		default:
			out.Values = append(out.Values, revealedValue{addressOf(at), value})
		}
	}
	return out
}

func (s *Session) secrets() map[string]bool {
	out := map[string]bool{}
	for _, definition := range s.opts.Gate.Definitions() {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			out[definition.GetKey()] = true
		}
	}
	return out
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

type otherValue struct {
	addressRequest
	Version   int64              `json:"version"`
	Class     string             `json:"class"`
	Reference *envgate.Reference `json:"reference,omitempty"`
	Value     *string            `json:"value,omitempty"`
	Error     string             `json:"error,omitempty"`
}

type otherResponse struct {
	Tier   string       `json:"tier"`
	Values []otherValue `json:"values"`
}

func (s *Session) handleOther(w http.ResponseWriter, r *http.Request) {
	if s.opts.Other == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("this session has no %s substrate to read", s.otherTier()))
		return
	}
	stored, err := s.opts.Other.List(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("read the %s values: %w", s.otherTier(), err))
		return
	}

	classes := s.classes()
	out := otherResponse{Tier: s.otherTier(), Values: []otherValue{}}
	var readable []envgate.Address
	for _, row := range stored {
		class, declared := classes[row.Cell.Key]
		if !declared || row.Reference != nil {
			continue
		}
		out.Values = append(out.Values, otherValue{
			addressRequest: addressOf(row.Address),
			Version:        row.Version,
			Class:          class,
		})
		if class != "secret" {
			readable = append(readable, row.Address)
		}
	}

	read := s.read(r.Context(), s.opts.Other, readable)
	values := make(map[envgate.Address]string, len(read.Values))
	for _, v := range read.Values {
		values[v.address()] = v.Value
	}
	problems := make(map[envgate.Address]string, len(read.Errors))
	for _, e := range read.Errors {
		problems[e.address()] = e.Error
	}
	for i := range out.Values {
		at := out.Values[i].address()
		if value, ok := values[at]; ok {
			out.Values[i].Value = &value
		}
		out.Values[i].Error = problems[at]
	}
	writeJSON(w, out)
}

var className = map[resourcesv1.VariableClass]string{
	resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:     "plain",
	resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE: "sensitive",
	resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:    "secret",
}

func (s *Session) classes() map[string]string {
	out := map[string]string{}
	for _, definition := range s.opts.Gate.Definitions() {
		out[definition.GetKey()] = className[definition.GetClass()]
	}
	return out
}

type copyRequest struct {
	Cells []struct {
		addressRequest
		Version *int64 `json:"version"`
	} `json:"cells"`
}

type copyOutcome struct {
	addressRequest
	Saved    bool   `json:"saved"`
	Conflict bool   `json:"conflict,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (s *Session) handleCopy(w http.ResponseWriter, r *http.Request) {
	if s.opts.Other == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("this session has no %s substrate to copy from", s.otherTier()))
		return
	}
	var req copyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	rows := make([]envgate.Address, 0, len(req.Cells))
	for _, cell := range req.Cells {
		rows = append(rows, cell.address())
	}
	read := s.read(r.Context(), s.opts.Other, rows)
	values := make(map[envgate.Address]string, len(read.Values))
	for _, v := range read.Values {
		values[v.address()] = v.Value
	}
	problems := make(map[envgate.Address]string, len(read.Errors))
	for _, e := range read.Errors {
		problems[e.address()] = e.Error
	}

	outcomes := make([]copyOutcome, 0, len(req.Cells))
	for _, cell := range req.Cells {
		at := cell.address()
		outcome := copyOutcome{addressRequest: cell.addressRequest}
		value, ok := values[at]
		switch {
		case !ok:
			outcome.Error = fmt.Sprintf("could not read the %s value: %s", s.otherTier(), problems[at])
		default:
			if err := s.writable(at); err != nil {
				outcome.Error = err.Error()
				break
			}
			err := s.opts.Store.Set(r.Context(), at, value, cell.Version)
			switch {
			case errors.Is(err, ErrStaleValue):
				outcome.Conflict = true
				outcome.Error = err.Error()
			case err != nil:
				outcome.Error = err.Error()
			default:
				outcome.Saved = true
				s.forget(at)
			}
		}
		outcomes = append(outcomes, outcome)
	}
	writeJSON(w, map[string]any{"results": outcomes})
}

func (s *Session) handleDone(w http.ResponseWriter, r *http.Request) {
	if s.opts.Recovery != nil {
		if err := s.opts.Gate.Prefetch(r.Context()); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		if err := s.opts.Gate.Check(); err != nil {
			fail(w, http.StatusConflict, fmt.Errorf("the deploy cannot resume yet: %w", err))
			return
		}
	}
	s.leave(w, nil)
}

func (s *Session) handleAbandon(w http.ResponseWriter, _ *http.Request) {
	s.leave(w, ErrAbandoned)
}

func (s *Session) handlePresence(w http.ResponseWriter, r *http.Request) {
	s.arrive()
	defer s.depart()
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	select {
	case <-r.Context().Done():
	case <-s.done:
	}
}

func (s *Session) arrive() {
	s.present.Lock()
	defer s.present.Unlock()
	s.pages++
	if s.departed != nil {
		s.departed.Stop()
		s.departed = nil
	}
}

func (s *Session) depart() {
	s.present.Lock()
	defer s.present.Unlock()
	s.pages--
	if s.pages > 0 {
		return
	}
	s.departed = time.AfterFunc(s.opts.Absence, func() {
		s.present.Lock()
		gone := s.pages == 0
		s.present.Unlock()
		if gone {
			_ = s.finish(ErrAbandoned)
		}
	})
}

func (s *Session) leave(w http.ResponseWriter, outcome error) {
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() { _ = s.finish(outcome) }()
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
		Tier:         s.tier(),
		Other:        s.otherTier(),
		Environments: s.opts.Environments,
		Matrix:       s.opts.Gate.Matrix(s.opts.Environments),
		Recovery:     s.opts.Recovery,
	})
}

func (s *Session) tier() string {
	if s.opts.Preview {
		return "preview"
	}
	return "production"
}

func (s *Session) otherTier() string {
	if s.opts.Preview {
		return "production"
	}
	return "preview"
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
