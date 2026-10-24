package terminal

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTableLinesUpEachColumnUnderItsHead(t *testing.T) {
	t.Parallel()

	table := NewTable(Column{Head: "PROJECT"}, Column{Head: "EDGE"}, Column{Head: "COST", Align: AlignRight})
	table.Row("go", "cloudflare", "1.00")
	table.Row("blog", NoValue, "14.60")

	want := "" +
		"  PROJECT  EDGE         COST\n" +
		"  go       cloudflare   1.00\n" +
		"  blog     —           14.60\n"
	if got := table.Render(Palette{}, "  "); got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
}

func TestTableMeasuresColoredAndLinkedCellsByWhatTheyShow(t *testing.T) {
	t.Parallel()

	colored := Palette{colored: true}
	linked := Presentation{Hyperlinks: true}.Hyperlink("svc", "https://console.example.com/svc")
	table := NewTable(Column{Head: "STATUS"}, Column{Head: "ID"}, Column{Head: "KIND"})
	table.Row(colored.Health(Healthy, "live"), linked, "run:service")
	table.Row("unpromoted", "a-much-longer-id", "run:job")

	lines := strings.Split(strings.TrimSuffix(table.Render(colored, ""), "\n"), "\n")
	at := strings.Index(ansi.Strip(lines[0]), "KIND")
	for _, line := range lines[1:] {
		plain := []rune(ansi.Strip(line))
		if !strings.HasPrefix(string(plain[at:]), "run:") {
			t.Errorf("row %q puts KIND at %d off its head, want it under KIND", plain, at)
		}
	}
}

func TestTableMutesItsHeadAndEveryMissingValue(t *testing.T) {
	t.Parallel()

	colored := Palette{colored: true}
	table := NewTable(Column{Head: "URL"})
	table.Row(NoValue)

	got := table.Render(colored, "")
	for _, want := range []string{colored.Muted("URL"), colored.Muted(NoValue)} {
		if !strings.Contains(got, want) {
			t.Errorf("table = %q, want %q muted", got, want)
		}
	}
}

func TestTableSpanAndRuleTakeNoColumnWidth(t *testing.T) {
	t.Parallel()

	table := NewTable(Column{Head: "NAME"}, Column{Head: "USAGE", Align: AlignRight})
	table.Span("a heading far wider than any column")
	table.Row("fn", "1.00")
	table.Rule()
	table.Row("Total", "1.00")

	want := "" +
		"NAME   USAGE\n" +
		"a heading far wider than any column\n" +
		"fn      1.00\n" +
		"────────────\n" +
		"Total   1.00\n"
	if got := table.Render(Palette{}, ""); got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
}

func TestTableWithoutAHeadPrintsOnlyItsRows(t *testing.T) {
	t.Parallel()

	table := NewTable(Column{}, Column{})
	table.Row("kms:key", "production")
	table.Row("storage:bucket", "ocel-prod-edge")

	want := "" +
		"kms:key         production\n" +
		"storage:bucket  ocel-prod-edge\n"
	if got := table.Render(Palette{}, ""); got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
}
