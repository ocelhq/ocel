package tracing

import (
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const TailBytes = 8 << 10

type Tail struct {
	Limit int

	mu      sync.Mutex
	ring    []byte
	next    int
	wrapped bool
}

func NewTail(text string) *Tail {
	tail := &Tail{}
	_, _ = io.WriteString(tail, text)
	return tail
}

func (t *Tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ring == nil {
		t.ring = make([]byte, t.limit())
	}
	written := len(p)
	if len(p) >= len(t.ring) {
		p = p[len(p)-len(t.ring):]
	}
	for len(p) > 0 {
		n := copy(t.ring[t.next:], p)
		p = p[n:]
		t.next += n
		if t.next == len(t.ring) {
			t.next, t.wrapped = 0, true
		}
	}
	return written, nil
}

func (t *Tail) limit() int {
	if t.Limit > 0 {
		return t.Limit
	}
	return TailBytes
}

func (t *Tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.text()
}

func (t *Tail) WholeLines() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	text := t.text()
	if !t.wrapped {
		return text
	}
	if _, rest, cut := strings.Cut(text, "\n"); cut {
		return rest
	}
	return text
}

func (t *Tail) text() string {
	if !t.wrapped {
		return string(t.ring[:t.next])
	}
	return startOnCharacter(string(t.ring[t.next:]) + string(t.ring[:t.next]))
}

func startOnCharacter(s string) string {
	for i := 0; i < len(s) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(s[i]) {
			return s[i:]
		}
	}
	return s
}
