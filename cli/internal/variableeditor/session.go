package variableeditor

import (
	"context"
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

	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/localrpc"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

var ErrAbandoned = errors.New("variables UI abandoned")

const AbandonedMessage = "the variables UI closed before the matrix was complete"

const DefaultAbandonAfter = 5 * time.Second

type Values interface {
	List(ctx context.Context) ([]variables.ValueMetadata, error)
	Reveal(ctx context.Context, rows []variables.Coordinate) (map[variables.Coordinate]string, error)
	Set(ctx context.Context, at variables.Coordinate, value string, expected *int64) (int64, error)
	Delete(ctx context.Context, at variables.Coordinate, expected *int64) (bool, error)
	History(ctx context.Context, at variables.Coordinate) ([]variables.Version, error)
}

type Recovery struct {
	Deploy  string           `json:"deploy"`
	Missing []variables.Cell `json:"missing"`
}

type Page struct {
	Slug         string               `json:"slug"`
	Tier         string               `json:"tier"`
	Other        string               `json:"other"`
	Environments []string             `json:"environments"`
	Matrix       variables.Matrix     `json:"matrix"`
	Recovery     *Recovery            `json:"recovery,omitempty"`
	EnvSource    *variables.EnvSource `json:"envSource,omitempty"`
}

type EnvSourceClient interface {
	DescribeEnvSource(ctx context.Context) (variables.EnvSource, error)
	SyncEnvSource(ctx context.Context) (variables.EnvSource, error)
	SetInEnvSource(ctx context.Context, at variables.Cell, value, description string) (awaitingApproval bool, err error)
}
type Options struct {
	Assets fs.FS

	Declarations *variables.Declarations

	Values      Values
	OtherValues Values
	EnvSource   EnvSourceClient

	Slug string
	Tier environmentv1.Tier

	Environments []string

	Recovery *Recovery

	AbandonAfter time.Duration
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

	viewersMu    sync.Mutex
	viewers      int
	abandonTimer *time.Timer
}

func Serve(ctx context.Context, opts Options) (*Session, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open a loopback port for the variables UI: %w", err)
	}

	if opts.AbandonAfter <= 0 {
		opts.AbandonAfter = DefaultAbandonAfter
	}
	s := &Session{
		Token:    localrpc.NewSessionToken(),
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
	api.HandleFunc("GET /api/state", s.handlePage)
	api.HandleFunc("PUT /api/value", s.handleSet)
	api.HandleFunc("DELETE /api/value", s.handleDelete)
	api.HandleFunc("POST /api/env-source/value", s.handleSetInEnvSource)
	api.HandleFunc("POST /api/reveal", s.handleReveal)
	api.HandleFunc("GET /api/history", s.handleHistory)
	api.HandleFunc("GET /api/other", s.handleOther)
	api.HandleFunc("POST /api/copy", s.handleCopy)
	api.HandleFunc("POST /api/done", s.handleDone)
	api.HandleFunc("POST /api/abandon", s.handleAbandon)
	api.HandleFunc("GET /api/presence", s.handleViewer)

	page := http.FileServerFS(s.opts.Assets)

	mux := http.NewServeMux()
	mux.Handle("/", page)
	mux.Handle("/api/", s.guard(api))
	return mux
}

func (s *Session) guard(next http.Handler) http.Handler {
	return localrpc.LoopbackGuard(s.listener.Addr().String(), s.Token, next)
}

func (s *Session) handlePage(w http.ResponseWriter, r *http.Request) {
	s.writePage(r.Context(), w)
}

type coordinateRequest struct {
	Key         string `json:"key"`
	Folder      string `json:"folder"`
	Environment string `json:"environment"`
}

func (a coordinateRequest) coordinate() variables.Coordinate {
	return variables.Coordinate{Cell: variables.Cell{Key: a.Key, Folder: a.Folder}, Environment: a.Environment}
}

func coordinateOf(at variables.Coordinate) coordinateRequest {
	return coordinateRequest{Key: at.Cell.Key, Folder: at.Cell.Folder, Environment: at.Environment}
}

type valueRequest struct {
	coordinateRequest
	Value   string `json:"value"`
	Version *int64 `json:"version"`
}

func (s *Session) handleSet(w http.ResponseWriter, r *http.Request) {
	var req valueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	at := req.coordinate()
	if err := s.writable(at); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.opts.Values.Set(r.Context(), at, req.Value, req.Version); err != nil {
		if errors.Is(err, variables.ErrStaleValue) {
			fail(w, http.StatusConflict, err)
			return
		}
		fail(w, http.StatusBadGateway, err)
		return
	}

	s.clearProblems(at)
	writeJSON(w, struct{}{})
}

func (s *Session) handleSetInEnvSource(w http.ResponseWriter, r *http.Request) {
	if s.opts.EnvSource == nil {
		fail(w, http.StatusNotFound, errors.New("this tier reads from no env source ocel can write a value into"))
		return
	}
	var req valueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	at := req.coordinate()
	if err := s.settableInEnvSource(at); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	awaiting, err := s.opts.EnvSource.SetInEnvSource(r.Context(), at.Cell, req.Value, s.description(at.Cell.Key))
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.clearProblems(at)
	writeJSON(w, map[string]bool{"awaitingApproval": awaiting})
}

func (s *Session) settableInEnvSource(at variables.Coordinate) error {
	if at.Environment != "" {
		return fmt.Errorf("a value for %s alone is stored by ocel, never by the env source: save it as an override for %s instead", at.Environment, at.Environment)
	}
	if slices.Contains(s.opts.Declarations.Scope().EnvSource.Credentials, at.Cell.Key) {
		return fmt.Errorf("%s is what ocel logs in to the env source with, so ocel stores it itself: save it here instead", at.Cell.Key)
	}
	return s.writable(at)
}

func (s *Session) description(key string) string {
	for _, definition := range s.opts.Declarations.Declared() {
		if definition.GetKey() == key {
			return definition.GetDescription()
		}
	}
	return ""
}

func (s *Session) handleDelete(w http.ResponseWriter, r *http.Request) {
	at := queryCoordinate(r)
	if err := addressable(at.Cell.Folder); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	expected, err := queryVersion(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.opts.Values.Delete(r.Context(), at, expected); err != nil {
		if errors.Is(err, variables.ErrStaleValue) {
			fail(w, http.StatusConflict, err)
			return
		}
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.clearProblems(at)
	writeJSON(w, struct{}{})
}

func (s *Session) writable(at variables.Coordinate) error {
	if err := addressable(at.Cell.Folder); err != nil {
		return err
	}
	if err := variables.RefuseImpliedInFolder(s.opts.Declarations.Scope(), at.Cell.Key, at.Cell.Folder); err != nil {
		return err
	}
	if err := variables.RefuseUnwritable(s.opts.Declarations.Declared(), at.Cell.Key, at.Cell.Folder); err != nil {
		return err
	}
	if at.Environment == "" || slices.Contains(s.opts.Environments, at.Environment) {
		return nil
	}
	return fmt.Errorf("no environment named %q exists, so nothing would ever read that value", at.Environment)
}

func (s *Session) clearProblems(at variables.Coordinate) {
	if at.Environment == "" {
		s.opts.Declarations.ClearProblems(at.Cell)
	}
}

type revealRequest struct {
	Cells []coordinateRequest `json:"cells"`
}

type revealedValue struct {
	coordinateRequest
	Value string `json:"value"`
}

type cellError struct {
	coordinateRequest
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
	rows := make([]variables.Coordinate, 0, len(req.Cells))
	for _, cell := range req.Cells {
		if secrets[cell.Key] {
			fail(w, http.StatusBadRequest, fmt.Errorf("%s is a secret, and a secret's value never reaches a browser; overwrite it here or read it with ocel env get --reveal", cell.Key))
			return
		}
		rows = append(rows, cell.coordinate())
	}
	writeJSON(w, s.reveal(r.Context(), s.opts.Values, rows))
}

func (s *Session) reveal(ctx context.Context, from Values, rows []variables.Coordinate) revealResponse {
	out := revealResponse{Values: []revealedValue{}, Errors: []cellError{}}
	if len(rows) == 0 {
		return out
	}
	found, err := from.Reveal(ctx, rows)
	for _, at := range rows {
		switch value, ok := found[at]; {
		case err != nil:
			out.Errors = append(out.Errors, cellError{coordinateOf(at), err.Error()})
		case !ok:
			out.Errors = append(out.Errors, cellError{coordinateOf(at), "no value could be read here"})
		default:
			out.Values = append(out.Values, revealedValue{coordinateOf(at), value})
		}
	}
	return out
}

func (s *Session) secrets() map[string]bool {
	out := map[string]bool{}
	for _, definition := range s.opts.Declarations.Declared() {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			out[definition.GetKey()] = true
		}
	}
	return out
}

func (s *Session) handleHistory(w http.ResponseWriter, r *http.Request) {
	versions, err := s.opts.Values.History(r.Context(), queryCoordinate(r))
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if versions == nil {
		versions = []variables.Version{}
	}
	writeJSON(w, map[string]any{"versions": versions})
}

type otherValue struct {
	coordinateRequest
	Version   int64                `json:"version"`
	Class     string               `json:"class"`
	Reference *variables.Reference `json:"reference,omitempty"`
	Value     *string              `json:"value,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type otherResponse struct {
	Tier   string       `json:"tier"`
	Values []otherValue `json:"values"`
}

func (s *Session) handleOther(w http.ResponseWriter, r *http.Request) {
	if s.opts.OtherValues == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("this session has no %s store to read", s.otherTier()))
		return
	}
	stored, err := s.opts.OtherValues.List(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("read the %s values: %w", s.otherTier(), err))
		return
	}

	classes := s.classes()
	out := otherResponse{Tier: s.otherTier(), Values: []otherValue{}}
	var readable []variables.Coordinate
	for _, row := range stored {
		class, declared := classes[row.Cell.Key]
		if !declared || row.Reference != nil {
			continue
		}
		out.Values = append(out.Values, otherValue{
			coordinateRequest: coordinateOf(row.Coordinate),
			Version:           row.Version,
			Class:             class,
		})
		if class != "secret" {
			readable = append(readable, row.Coordinate)
		}
	}

	read := s.reveal(r.Context(), s.opts.OtherValues, readable)
	values := make(map[variables.Coordinate]string, len(read.Values))
	for _, v := range read.Values {
		values[v.coordinate()] = v.Value
	}
	problems := make(map[variables.Coordinate]string, len(read.Errors))
	for _, e := range read.Errors {
		problems[e.coordinate()] = e.Error
	}
	for i := range out.Values {
		at := out.Values[i].coordinate()
		if value, ok := values[at]; ok {
			out.Values[i].Value = &value
		}
		out.Values[i].Error = problems[at]
	}
	writeJSON(w, out)
}

func (s *Session) classes() map[string]string {
	out := map[string]string{}
	for _, definition := range s.opts.Declarations.Declared() {
		out[definition.GetKey()] = variables.ClassName(definition.GetClass())
	}
	return out
}

type copyRequest struct {
	Cells []struct {
		coordinateRequest
		Version *int64 `json:"version"`
	} `json:"cells"`
}

type copyOutcome struct {
	coordinateRequest
	Saved    bool   `json:"saved"`
	Conflict bool   `json:"conflict,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (s *Session) handleCopy(w http.ResponseWriter, r *http.Request) {
	if s.opts.OtherValues == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("this session has no %s store to copy from", s.otherTier()))
		return
	}
	var req copyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("read this request: %w", err))
		return
	}
	rows := make([]variables.Coordinate, 0, len(req.Cells))
	for _, cell := range req.Cells {
		rows = append(rows, cell.coordinate())
	}
	read := s.reveal(r.Context(), s.opts.OtherValues, rows)
	values := make(map[variables.Coordinate]string, len(read.Values))
	for _, v := range read.Values {
		values[v.coordinate()] = v.Value
	}
	problems := make(map[variables.Coordinate]string, len(read.Errors))
	for _, e := range read.Errors {
		problems[e.coordinate()] = e.Error
	}

	outcomes := make([]copyOutcome, 0, len(req.Cells))
	for _, cell := range req.Cells {
		at := cell.coordinate()
		outcome := copyOutcome{coordinateRequest: cell.coordinateRequest}
		value, ok := values[at]
		switch {
		case !ok:
			outcome.Error = fmt.Sprintf("could not read the %s value: %s", s.otherTier(), problems[at])
		default:
			if err := s.writable(at); err != nil {
				outcome.Error = err.Error()
				break
			}
			_, err := s.opts.Values.Set(r.Context(), at, value, cell.Version)
			switch {
			case errors.Is(err, variables.ErrStaleValue):
				outcome.Conflict = true
				outcome.Error = err.Error()
			case err != nil:
				outcome.Error = err.Error()
			default:
				outcome.Saved = true
				s.clearProblems(at)
			}
		}
		outcomes = append(outcomes, outcome)
	}
	writeJSON(w, map[string]any{"results": outcomes})
}

func (s *Session) handleDone(w http.ResponseWriter, r *http.Request) {
	if s.opts.Recovery != nil {
		if s.opts.EnvSource != nil {
			if _, err := s.opts.EnvSource.SyncEnvSource(r.Context()); err != nil {
				fail(w, http.StatusBadGateway, fmt.Errorf("read the env source again: %w", err))
				return
			}
		}
		if err := s.opts.Declarations.Prefetch(r.Context()); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		if err := s.opts.Declarations.RefuseIncomplete(); err != nil {
			fail(w, http.StatusConflict, fmt.Errorf("the deploy cannot resume yet: %w", err))
			return
		}
	}
	s.respondAndFinish(w, nil)
}

func (s *Session) handleAbandon(w http.ResponseWriter, _ *http.Request) {
	s.respondAndFinish(w, ErrAbandoned)
}

func (s *Session) handleViewer(w http.ResponseWriter, r *http.Request) {
	s.join()
	defer s.leave()
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

func (s *Session) join() {
	s.viewersMu.Lock()
	defer s.viewersMu.Unlock()
	s.viewers++
	if s.abandonTimer != nil {
		s.abandonTimer.Stop()
		s.abandonTimer = nil
	}
}

func (s *Session) leave() {
	s.viewersMu.Lock()
	defer s.viewersMu.Unlock()
	s.viewers--
	if s.viewers > 0 {
		return
	}
	s.abandonTimer = time.AfterFunc(s.opts.AbandonAfter, func() {
		s.viewersMu.Lock()
		gone := s.viewers == 0
		s.viewersMu.Unlock()
		if gone {
			_ = s.finish(ErrAbandoned)
		}
	})
}

func (s *Session) respondAndFinish(w http.ResponseWriter, outcome error) {
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
	return variables.ValidateFolder(folder)
}

func queryCoordinate(r *http.Request) variables.Coordinate {
	return variables.Coordinate{
		Cell:        variables.Cell{Key: r.URL.Query().Get("key"), Folder: r.URL.Query().Get("folder")},
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

func (s *Session) writePage(ctx context.Context, w http.ResponseWriter) {
	if err := s.opts.Declarations.Prefetch(ctx); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	environments := s.opts.Environments
	if environments == nil {
		environments = []string{}
	}
	out := Page{
		Slug:         s.opts.Slug,
		Tier:         s.tier(),
		Other:        s.otherTier(),
		Environments: environments,
		Matrix:       s.opts.Declarations.Matrix(s.opts.Environments),
		Recovery:     s.opts.Recovery,
	}
	if s.opts.EnvSource != nil {
		described, err := s.opts.EnvSource.DescribeEnvSource(ctx)
		if err != nil {
			fail(w, http.StatusBadGateway, fmt.Errorf("read which env source this tier reads from: %w", err))
			return
		}
		out.EnvSource = &described
	}
	writeJSON(w, out)
}

func (s *Session) tier() string {
	if s.opts.Tier == environmentv1.Tier_TIER_PREVIEW {
		return "preview"
	}
	return "production"
}

func (s *Session) otherTier() string {
	if s.opts.Tier == environmentv1.Tier_TIER_PREVIEW {
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
