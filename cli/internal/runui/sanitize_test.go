package runui

import "testing"

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
