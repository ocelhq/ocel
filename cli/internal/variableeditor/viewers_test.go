package variableeditor_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/variableeditor"
)

const abandonAfter = 50 * time.Millisecond

func serveAbandonedQuickly(t *testing.T) *variableeditor.Session {
	t.Helper()
	store := newFakeStore()
	return serveWith(t, context.Background(), variableeditor.Options{
		Declarations: discovered(t, store, def("API_URL")),
		Values:       store,
		AbandonAfter: abandonAfter,
	})
}

func openViewer(t *testing.T, s *variableeditor.Session) context.CancelFunc {
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
		t.Fatalf("GET /api/presence = %d, want 200 with the connection kept open", resp.StatusCode)
	}
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})
	return cancel
}

func stillOpen(t *testing.T, s *variableeditor.Session, window time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	if err := s.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v within %s, want the session still open", err, window)
	}
}

func abandonedWithin(t *testing.T, s *variableeditor.Session, bound time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	if err := s.Wait(ctx); !errors.Is(err, variableeditor.ErrAbandoned) {
		t.Fatalf("Wait = %v within %s, want %v", err, bound, variableeditor.ErrAbandoned)
	}
}

func TestASessionIsAbandonedOnlyOnceEveryPageHasLeftForAWhile(t *testing.T) {
	t.Parallel()

	t.Run("a page that drops its connection and never returns has abandoned the session", func(t *testing.T) {
		t.Parallel()
		s := serveAbandonedQuickly(t)
		leave := openViewer(t, s)
		stillOpen(t, s, 3*abandonAfter)

		leave()
		abandonedWithin(t, s, 20*abandonAfter)
	})

	t.Run("a page sitting idle with its connection open is never treated as gone", func(t *testing.T) {
		t.Parallel()
		s := serveAbandonedQuickly(t)
		openViewer(t, s)
		stillOpen(t, s, 6*abandonAfter)
	})

	t.Run("a reload that returns inside the grace keeps the session alive", func(t *testing.T) {
		t.Parallel()
		s := serveAbandonedQuickly(t)
		leave := openViewer(t, s)
		leave()
		openViewer(t, s)
		stillOpen(t, s, 6*abandonAfter)
	})

	t.Run("a second tab keeps the session alive after the first closes", func(t *testing.T) {
		t.Parallel()
		s := serveAbandonedQuickly(t)
		first := openViewer(t, s)
		openViewer(t, s)
		first()
		stillOpen(t, s, 6*abandonAfter)
	})

	t.Run("a session nobody has visited waits for them", func(t *testing.T) {
		t.Parallel()
		s := serveAbandonedQuickly(t)
		stillOpen(t, s, 6*abandonAfter)
	})

	t.Run("finishing the session releases the page's connection", func(t *testing.T) {
		t.Parallel()
		s := serveAbandonedQuickly(t)
		openViewer(t, s)
		if resp := request(t, s, http.MethodPost, "/api/abandon", nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/abandon = %d", resp.StatusCode)
		}
		abandonedWithin(t, s, 20*abandonAfter)
	})
}
