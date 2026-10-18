package runsconformance

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/runs"
)

type answer struct {
	status int
	body   string
}

var answeredOK = answer{status: http.StatusOK, body: `{"ok":true}`}

type worker struct {
	url string

	mu         sync.Mutex
	answer     func(r *http.Request, delivered *topicv1.Envelope, call int) answer
	envelopes  []*topicv1.Envelope
	running    int
	maxRunning int
}

func newWorker(t *testing.T, answer func(r *http.Request, delivered *topicv1.Envelope, call int) answer) *worker {
	t.Helper()
	w := &worker{answer: answer}
	server := httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(server.Close)
	w.url = server.URL
	return w
}

func (w *worker) serve(rw http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	delivered, err := runs.DecodeEnvelope(body)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	w.envelopes = append(w.envelopes, delivered)
	call := len(w.envelopes)
	w.running++
	w.maxRunning = max(w.maxRunning, w.running)
	answering := w.answer
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.running--
		w.mu.Unlock()
	}()
	answered := answering(r, delivered, call)
	rw.WriteHeader(answered.status)
	_, _ = io.WriteString(rw, answered.body)
}

func (w *worker) answerWith(answer func(r *http.Request, delivered *topicv1.Envelope, call int) answer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.answer = answer
}

func (w *worker) delivered() []*topicv1.Envelope {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*topicv1.Envelope(nil), w.envelopes...)
}

func (w *worker) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.envelopes)
}

func (w *worker) mostAtOnce() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxRunning
}

func answerAlways(answered answer) func(*http.Request, *topicv1.Envelope, int) answer {
	return func(*http.Request, *topicv1.Envelope, int) answer { return answered }
}
