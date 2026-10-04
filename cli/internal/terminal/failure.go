package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func PrintFailure(w io.Writer, err error) {
	for _, line := range failureLines(PaletteFor(w), "", err.Error()) {
		fmt.Fprintln(w, line)
	}
}

const unencodableFailureJSON = `{"ok":false,"error":{"code":"internal","message":"the error could not be encoded as JSON"}}`

func PrintFailureJSON(w io.Writer, failure *streamv1.RunError) {
	var document bytes.Buffer
	body, err := protojson.Marshal(failure)
	if err == nil {
		err = json.Compact(&document, fmt.Appendf(nil, `{"ok":false,"error":%s}`, body))
	}
	if err != nil {
		fmt.Fprintln(w, unencodableFailureJSON)
		return
	}
	fmt.Fprintln(w, document.String())
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
