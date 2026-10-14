package mountedgate

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

const (
	CacheFor    = 5 * time.Second
	ReadTimeout = 2 * time.Second
)

var ErrReadTimedOut = errors.New("the mounted gate was not read in time")

type reading struct {
	held *gate.Gate
	err  error
}

type Source struct {
	read    func() ([]byte, error)
	now     func() time.Time
	timeout time.Duration

	mu      sync.Mutex
	loaded  bool
	at      time.Time
	current reading
	loading chan struct{}
}

func New(read func() ([]byte, error), now func() time.Time) *Source {
	return &Source{read: read, now: now, timeout: ReadTimeout}
}

func FromEnv(env []string) (*Source, []string) {
	var file, inline *Source
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case gate.MountedFileVar:
			file = New(func() ([]byte, error) { return os.ReadFile(value) }, time.Now)
		case gate.MountedInlineVar:
			inline = New(func() ([]byte, error) { return []byte(value), nil }, time.Now)
		default:
			kept = append(kept, entry)
		}
	}
	if file != nil {
		return file, kept
	}
	return inline, kept
}

func (s *Source) Current() (*gate.Gate, error) {
	s.mu.Lock()
	now := s.now()
	if s.loaded && now.Sub(s.at) < CacheFor {
		current := s.current
		s.mu.Unlock()
		return current.held, current.err
	}
	loading := s.loading
	if loading == nil {
		loading = make(chan struct{})
		s.loading = loading
		go s.refresh(now, loading)
	}
	s.mu.Unlock()

	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case <-loading:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.current.held, s.current.err
	case <-timer.C:
		return nil, ErrReadTimedOut
	}
}

func (s *Source) refresh(started time.Time, done chan struct{}) {
	held, err := s.load()
	s.mu.Lock()
	s.current = reading{held: held, err: err}
	s.loaded, s.at, s.loading = true, started, nil
	s.mu.Unlock()
	close(done)
}

func (s *Source) load() (*gate.Gate, error) {
	contents, err := s.read()
	if err != nil {
		return nil, err
	}
	return gate.DecodeMounted(contents)
}
