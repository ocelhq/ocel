package tracing_test

import (
	"io"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/ocelhq/ocel/pkg/tracing"
)

func TestATailHoldsEverythingWrittenWhileItFits(t *testing.T) {
	tail := &tracing.Tail{}
	_, _ = io.WriteString(tail, "first\n")
	_, _ = io.WriteString(tail, "second\n")
	if got := tail.String(); got != "first\nsecond\n" {
		t.Errorf("String() = %q, want both writes", got)
	}
}

func TestATailKeepsTheLastBytesWrittenStartingOnACharacter(t *testing.T) {
	tail := &tracing.Tail{}
	var whole strings.Builder
	for i := range 3000 {
		line := "é line " + strings.Repeat("x", i%7) + "\n"
		whole.WriteString(line)
		_, _ = io.WriteString(tail, line)
	}

	got := tail.String()
	if !strings.HasSuffix(whole.String(), got) || len(got) > tracing.TailBytes || len(got) < tracing.TailBytes-utf8.UTFMax {
		t.Errorf("String() holds %d bytes, want the last %d written", len(got), tracing.TailBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("String() starts inside a character")
	}
}

func TestOneWriteLongerThanATailLeavesItsLastBytes(t *testing.T) {
	tail := &tracing.Tail{}
	long := strings.Repeat("a", tracing.TailBytes) + "end"
	if n, err := io.WriteString(tail, long); n != len(long) || err != nil {
		t.Fatalf("Write() = %d, %v, want every byte taken", n, err)
	}
	if got := tail.String(); got != long[len(long)-tracing.TailBytes:] {
		t.Errorf("String() holds %d bytes, want the last %d of the write", len(got), tracing.TailBytes)
	}
}

func TestATailTakesWritesFromSeveralGoroutines(t *testing.T) {
	tail := &tracing.Tail{}
	var writers sync.WaitGroup
	for range 8 {
		writers.Go(func() {
			for range 100 {
				_, _ = io.WriteString(tail, "line\n")
			}
		})
	}
	writers.Wait()
	if got := len(tail.String()); got != len("line\n")*800 {
		t.Errorf("String() holds %d bytes, want all %d written", got, len("line\n")*800)
	}
}

func TestATailGivenALimitKeepsThatManyOfTheLastBytes(t *testing.T) {
	tail := &tracing.Tail{Limit: 8}
	for range 1000 {
		_, _ = io.WriteString(tail, strings.Repeat("x", 100))
	}
	_, _ = io.WriteString(tail, "boom\n")
	if got := tail.String(); got != "xxxboom\n" {
		t.Errorf("String() = %q, want the last 8 bytes", got)
	}
	_, _ = io.WriteString(tail, strings.Repeat("y", 1<<20)+"end")
	if got := tail.String(); got != "yyyyyend" {
		t.Errorf("String() = %q, want the last 8 bytes of one long write", got)
	}
}

func TestANewTailHoldsTheLastBytesOfTheTextItIsMadeFrom(t *testing.T) {
	long := strings.Repeat("a", tracing.TailBytes) + "end"
	if got := tracing.NewTail(long).String(); got != long[len(long)-tracing.TailBytes:] {
		t.Errorf("String() holds %d bytes, want the last %d of the text", len(got), tracing.TailBytes)
	}
}

func TestTheWholeLinesOfATailDropTheLineItsLimitCutAndKeepEveryLineWhenNoneWasCut(t *testing.T) {
	t.Parallel()
	cut := &tracing.Tail{Limit: 16}
	_, _ = io.WriteString(cut, "first line\nsecond\nthird\n")
	if got := cut.WholeLines(); got != "second\nthird\n" {
		t.Errorf("WholeLines() = %q, want the lines the limit kept whole", got)
	}

	whole := &tracing.Tail{Limit: 64}
	_, _ = io.WriteString(whole, "first line\nsecond\n")
	if got := whole.WholeLines(); got != "first line\nsecond\n" {
		t.Errorf("WholeLines() = %q, want every line written", got)
	}

	long := &tracing.Tail{Limit: 8}
	_, _ = io.WriteString(long, "one line longer than the limit")
	if got := long.WholeLines(); got != "he limit" {
		t.Errorf("WholeLines() = %q, want the end of the one line the limit cut", got)
	}
}
