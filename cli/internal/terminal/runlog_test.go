package terminal

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestARunLogWritesASpansLinesAsTheyArriveDebugIncludedAndWithoutColour(t *testing.T) {
	t.Setenv("FORCE_COLOR", "1")
	var out bytes.Buffer
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	_, run := onABus(t, context.Background(), c.now, newRunLog(&out, nil))
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	_, web := build.Child(t.Context(), "web", progress.Building.Title("app web"))
	w := web.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDOUT)
	if _, err := w.Write([]byte("Compiled successfully\n")); err != nil {
		t.Fatal(err)
	}
	web.Debug("cache restored from .next/cache")

	before := out.String()
	for _, want := range []string{"Building app web", "Compiled successfully", "cache restored from .next/cache"} {
		if !strings.Contains(before, want) {
			t.Errorf("the log before the span ended =\n%s\nwant it to hold %q", before, want)
		}
	}

	c.pass(2 * time.Second)
	web.End(nil)
	if got := strings.TrimPrefix(out.String(), before); !strings.Contains(got, "Built app web in 2s") {
		t.Errorf("the log after the span ended added\n%s\nwant the span's end", got)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("the log holds terminal escapes:\n%q", out.String())
	}
}
