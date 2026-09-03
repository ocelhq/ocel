package varsui_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/varsui"
)

const absence = 50 * time.Millisecond

func servePresent(t *testing.T) *varsui.Session {
	t.Helper()
	store := newFakeStore()
	return serveWith(t, context.Background(), varsui.Options{
		Gate:    discovered(t, store, def("API_URL")),
		Store:   store,
		Absence: absence,
	})
}

func attend(t *testing.T, s *varsui.Session) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := newRequest(t, s, http.MethodGet, "/api/presence", nil).WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET /api/presence: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET /api/presence = %d, want 200 held open", resp.StatusCode)
	}
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})
	return cancel
}

func stillOpen(t *testing.T, s *varsui.Session, window time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	if err := s.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v within %s, want the session still open", err, window)
	}
}

func abandonedWithin(t *testing.T, s *varsui.Session, bound time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	if err := s.Wait(ctx); !errors.Is(err, varsui.ErrAbandoned) {
		t.Fatalf("Wait = %v within %s, want %v", err, bound, varsui.ErrAbandoned)
	}
}

func TestPresence(t *testing.T) {
	t.Parallel()

	t.Run("a page that drops its connection and never returns has abandoned the session", func(t *testing.T) {
		t.Parallel()
		s := servePresent(t)
		leave := attend(t, s)
		stillOpen(t, s, 3*absence)

		leave()
		abandonedWithin(t, s, 20*absence)
	})

	t.Run("a page sitting idle with its connection open is never treated as gone", func(t *testing.T) {
		t.Parallel()
		s := servePresent(t)
		attend(t, s)
		stillOpen(t, s, 6*absence)
	})

	t.Run("a reload that returns inside the grace keeps the session alive", func(t *testing.T) {
		t.Parallel()
		s := servePresent(t)
		leave := attend(t, s)
		leave()
		attend(t, s)
		stillOpen(t, s, 6*absence)
	})

	t.Run("a second tab keeps the session alive after the first closes", func(t *testing.T) {
		t.Parallel()
		s := servePresent(t)
		first := attend(t, s)
		attend(t, s)
		first()
		stillOpen(t, s, 6*absence)
	})

	t.Run("a session nobody has visited waits for them", func(t *testing.T) {
		t.Parallel()
		s := servePresent(t)
		stillOpen(t, s, 6*absence)
	})

	t.Run("finishing the session releases the page's connection", func(t *testing.T) {
		t.Parallel()
		s := servePresent(t)
		attend(t, s)
		if resp := request(t, s, http.MethodPost, "/api/abandon", nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/abandon = %d", resp.StatusCode)
		}
		abandonedWithin(t, s, 20*absence)
	})
}
