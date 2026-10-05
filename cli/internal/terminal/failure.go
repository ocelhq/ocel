package terminal

import (
	"fmt"
	"io"
	"strings"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func PrintFailure(w io.Writer, err error) {
	for _, line := range failureLines(PaletteFor(w), "", err.Error()) {
		fmt.Fprintln(w, line)
	}
}

const unencodableFailureJSON = `{"ok":false,"error":{"code":"internal","message":"the error could not be encoded as JSON","retryable":false}}`

func PrintFailureJSON(w io.Writer, failure *streamv1.RunError) {
	document, err := marshalEnvelope(false, failure)
	if err != nil {
		fmt.Fprintln(w, unencodableFailureJSON)
		return
	}
	_, _ = w.Write(document)
}

func failureLines(p Palette, headline, detail string) []string {
	lines := strings.Split(strings.TrimRight(detail, "\n"), "\n")
	head := failGlyph + " " + headline
	switch {
	case headline == "":
		head += lines[0]
	case lines[0] != "":
		head += " — " + lines[0]
	}
	out := []string{p.FailureBold(head)}
	for _, line := range lines[1:] {
		out = append(out, blockIndent+line)
	}
	return out
}
