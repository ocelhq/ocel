package childprocess

import (
	"strings"
	"testing"
)

func TestATailKeepsOnlyTheLastBytesWrittenToIt(t *testing.T) {
	t.Parallel()

	tail := &Tail{Limit: 8}
	for range 1000 {
		if _, err := tail.Write([]byte(strings.Repeat("x", 100))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tail.Write([]byte("boom\n")); err != nil {
		t.Fatal(err)
	}

	if got := tail.String(); got != "xxxboom" {
		t.Errorf("String() = %q, want the last 8 bytes", got)
	}
	if cap(tail.buf) > 1024 {
		t.Errorf("the tail holds %d bytes, want it bounded near its limit however much was written", cap(tail.buf))
	}

	if _, err := tail.Write([]byte(strings.Repeat("y", 1<<20) + "end")); err != nil {
		t.Fatal(err)
	}
	if got := tail.String(); got != "yyyyyend" {
		t.Errorf("String() after one large write = %q, want its last 8 bytes", got)
	}
	if cap(tail.buf) > 1024 {
		t.Errorf("one large write left the tail holding %d bytes, want it bounded near its limit", cap(tail.buf))
	}
}
