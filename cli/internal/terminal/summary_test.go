package terminal

import (
	"bytes"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestAFailedRunsHumanSummaryShowsOnlyItsHeadlineAndDetailWhateverItsErrorCarries(t *testing.T) {
	t.Parallel()

	for name, runError := range map[string]*streamv1.RunError{
		"no error": nil,
		"coded": {
			Code: "project.no_config", Message: "no ocel.json", Hint: proto.String("run `ocel init`"),
			DocsUrl: proto.String("https://ocel.dev/docs/errors/project.no_config"), Retryable: true,
		},
	} {
		var out bytes.Buffer
		sink := newTranscript(&out, Presentation{GitHubActions: true}, nil)
		sink.Receive(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_ERROR}, Cli: &streamv1.RunEvent_Summary{Summary: &streamv1.RunSummary{
			Headline: "Deploy failed", DurationMs: 3000, Detail: "no ocel.json", Error: runError,
		}}})

		want := "✗ Deploy failed in 3s — no ocel.json\n" +
			"::error::Deploy failed — no ocel.json\n"
		if got := out.String(); got != want {
			t.Errorf("%s: got  %q\nwant %q", name, got, want)
		}
	}
}

func TestALogUnderAFolderStartingWithTwoDotsIsNamedRelativeToTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if got, want := relLog(filepath.Join(dir, "..logs", "run.log")), filepath.Join("..logs", "run.log"); got != want {
		t.Errorf("relLog = %q, want %q", got, want)
	}
	above := filepath.Join(filepath.Dir(dir), "run.log")
	if got := relLog(above); got != above {
		t.Errorf("relLog of a log above the working directory = %q, want its absolute path %q", got, above)
	}
}
