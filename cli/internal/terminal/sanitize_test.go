package terminal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizingDropsColourAndCursorEscapes(t *testing.T) {
	t.Parallel()

	got, ok := sanitize("\x1b[2K\x1b[1G\x1b[32mCompiled\x1b[0m successfully")
	if want := "Compiled successfully"; got != want || !ok {
		t.Fatalf("sanitize() = %q, %v, want %q, true", got, ok, want)
	}
}

func TestADockerProgressLineKeepsOnlyItsLastRewrite(t *testing.T) {
	t.Parallel()

	raw := "=> [builder 5/6] COPY . .  0.4s\r\x1b[2K=> [builder 6/6] RUN npm run build  12.3s\r\x1b[2K"
	got, ok := sanitize(raw)
	if want := "=> [builder 6/6] RUN npm run build  12.3s"; got != want || !ok {
		t.Fatalf("sanitize(%q) = %q, %v, want %q, true", raw, got, ok, want)
	}
}

func TestSanitizingDropsControlCharacters(t *testing.T) {
	t.Parallel()

	got, ok := sanitize("done\a\x00 in\x7f 3\u009bs\x08")
	if want := "done in 3s"; got != want || !ok {
		t.Fatalf("sanitize() = %q, %v, want %q, true", got, ok, want)
	}
}

func TestSanitizingExpandsTabsToTheNextEightColumnStop(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"api\tready":   "api     ready",
		"中文\tready":    "中文    ready",
		"12345678\tok": "12345678        ok",
	}
	for raw, want := range cases {
		if got, ok := sanitize(raw); got != want || !ok {
			t.Errorf("sanitize(%q) = %q, %v, want %q, true", raw, got, ok, want)
		}
	}
}

func TestABlankLineIsSkipped(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "   \t ", "\x1b[0m\x1b[2K", "\r\x1b[2K\r"} {
		if got, ok := sanitize(raw); ok {
			t.Errorf("sanitize(%q) = %q, true, want it skipped: it shows nothing", raw, got)
		}
	}
}

func TestALineOfOnlyBoxDrawingIsSkipped(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"╭────────────╮", "│            │", "  ├──┼──┤  ", "\x1b[90m└────┘\x1b[0m"} {
		if got, ok := sanitize(raw); ok {
			t.Errorf("sanitize(%q) = %q, true, want it skipped: a frame says nothing", raw, got)
		}
	}
}

func TestALineWithTextInsideABoxIsKept(t *testing.T) {
	t.Parallel()

	got, ok := sanitize("│ Update available │")
	if want := "│ Update available │"; got != want || !ok {
		t.Fatalf("sanitize() = %q, %v, want %q, true", got, ok, want)
	}
}

func TestSanitizingDropsTheIndentAndTrailingSpaceAroundTheText(t *testing.T) {
	t.Parallel()

	got, ok := sanitize("\t   Compiling serde v1.0.210   ")
	if want := "Compiling serde v1.0.210"; got != want || !ok {
		t.Fatalf("sanitize() = %q, %v, want %q, true", got, ok, want)
	}
}

func TestSanitizeLogTextStripsEscapeSequencesAndKeepsTheVisibleText(t *testing.T) {
	t.Parallel()

	got := SanitizeLogText("\x1b]0;owned\a\x1b[31mGET /\x1b[0m ok")
	if want := "GET / ok"; got != want {
		t.Fatalf("SanitizeLogText() = %q, want %q", got, want)
	}
}

func TestSanitizeLogTextShowsALoneControlCharacterAsAnEscapeInsteadOfObeyingIt(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"real\rforged":      `real\rforged`,
		"bell\a":            `bell\u0007`,
		"back\bspace":       `back\u0008space`,
		"del\x7f":           `del\u007f`,
		"csi\u009b2J":       `csi\u009b2J`,
		"bidi\u202eexe.txt": `bidi\u202eexe.txt`,
		"isolate\u2067x":    `isolate\u2067x`,
	}
	for raw, want := range cases {
		if got := SanitizeLogText(raw); got != want {
			t.Errorf("SanitizeLogText(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestSanitizeLogTextKeepsIndentationAndJoinsLinesWithAnEscapedNewline(t *testing.T) {
	t.Parallel()

	got := SanitizeLogText("Error: boom\n    at run (app.js:1)\n\tat main\n")
	if want := "Error: boom\\n    at run (app.js:1)\\n\tat main"; got != want {
		t.Fatalf("SanitizeLogText() = %q, want %q", got, want)
	}
}

func TestSanitizeLogTextKeepsJoinersThatBuildACharacter(t *testing.T) {
	t.Parallel()

	family := "\U0001F468\u200d\U0001F469\u200d\U0001F467"
	if got := SanitizeLogText(family); got != family {
		t.Fatalf("SanitizeLogText() = %q, want %q", got, family)
	}
}

func TestEscapeControlsLeavesEncodedJSONDecodingToTheSameText(t *testing.T) {
	t.Parallel()

	raw := "\u009b2J \u202e \x7f \x1b[31m"
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	escaped := EscapeControls(string(encoded))
	if strings.ContainsAny(escaped, "\u009b\u202e\x7f\x1b") {
		t.Errorf("EscapeControls() = %q, want no control character left raw", escaped)
	}
	var decoded string
	if err := json.Unmarshal([]byte(escaped), &decoded); err != nil || decoded != raw {
		t.Errorf("decoded %q, %v, want %q", decoded, err, raw)
	}
}

func TestSanitizingDropsJoinersVariationSelectorsAndOtherInvisibleCharacters(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"👨\u200d💻 compiling":                           "👨💻 compiling",
		"☁\ufe0f uploading ✓\ufe0e":                    "☁ uploading ✓",
		"a\u200bb\u200cc\u200ed\u200ff":                "abcdf",
		"\u202aleft\u202b\u202c\u202d\u202eright":      "leftright",
		"line\u2028separator\u2029paragraph":           "lineseparatorparagraph",
		"\u2060word\u2061joiner\u2062and\u2063\u2064s": "wordjoinerands",
		"\ufeffbom": "bom",
	}
	for raw, want := range cases {
		if got, ok := sanitize(raw); got != want || !ok {
			t.Errorf("sanitize(%q) = %q, %v, want %q, true", raw, got, ok, want)
		}
	}
}
