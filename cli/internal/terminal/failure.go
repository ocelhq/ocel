package terminal

import (
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

func PrintFailureJSON(w io.Writer, failure *streamv1.RunError) {
	body, err := protojson.Marshal(failure)
	if err != nil {
		return
	}
	document, err := json.Marshal(struct {
		OK    bool            `json:"ok"`
		Error json.RawMessage `json:"error"`
	}{Error: body})
	if err != nil {
		return
	}
	fmt.Fprintln(w, string(document))
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
