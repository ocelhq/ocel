package childprocess

import "strings"

type Tail struct {
	Limit int
	buf   []byte
}

func (t *Tail) Write(p []byte) (int, error) {
	if len(p) >= t.Limit {
		t.buf = append(t.buf[:0], p[len(p)-t.Limit:]...)
		return len(p), nil
	}
	if extra := len(t.buf) + len(p) - t.Limit; extra > 0 {
		t.buf = append(t.buf[:0], t.buf[extra:]...)
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

func (t *Tail) String() string { return strings.TrimSpace(string(t.buf)) }
